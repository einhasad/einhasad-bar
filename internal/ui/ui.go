// Package ui owns the menu-bar presence: one systray icon per project, each with
// an attached frameless webview popover that updates live. Unlike a native menu
// (which freezes while open because macOS blocks the main dispatch queue during
// menu tracking), a webview is a normal window — so the Go side can push state to
// it via events and it re-renders in real time.
//
// Wire protocol (no bindings toolchain needed — pure events, JSON strings):
//
//	Go → JS:  Emit("eb:state", <Snapshot JSON>)        every tick + after actions
//	JS → Go:  Emit("eb:action", {op,project,service})  on every button click
package ui

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/einhasad/einhasad-bar/internal/config"
	"github.com/einhasad/einhasad-bar/internal/paths"
	"github.com/einhasad/einhasad-bar/internal/state"
	"github.com/einhasad/einhasad-bar/internal/supervisor"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// Icons holds the status-dot images keyed by aggregate stack state.
type Icons struct {
	Idle     []byte
	Busy     []byte
	Mixed    []byte
	Starting []byte
}

// Controller owns the trays/windows and drives state emission.
type Controller struct {
	app      *application.App
	sup      *supervisor.Supervisor
	icons    Icons
	projects []config.Project
	trays    map[string]*application.SystemTray
	lastIcon map[string]string
	locks    sync.Map // projectID → *sync.Mutex, serialises actions per project
}

// New builds a controller, creates one tray + popover per project, and wires the
// JS→Go action listener. Call Refresh once the app is running, then on a ticker.
func New(app *application.App, sup *supervisor.Supervisor, icons Icons, projects []config.Project) *Controller {
	c := &Controller{
		app:      app,
		sup:      sup,
		icons:    icons,
		projects: projects,
		trays:    map[string]*application.SystemTray{},
		lastIcon: map[string]string{},
	}
	for _, proj := range projects {
		c.addProject(proj)
	}
	app.Event.On("eb:action", c.onAction)
	return c
}

func (c *Controller) addProject(proj config.Project) {
	window := c.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:          proj.ID + "-panel",
		URL:           "/?p=" + proj.ID,
		Width:         330,
		Height:        420,
		Frameless:     true,
		AlwaysOnTop:   true,
		Hidden:        true,
		DisableResize: true,
		Windows:       application.WindowsWindow{HiddenOnTaskbar: true},
	})
	// Popover behaviour: hide when it loses focus (click elsewhere), but debounce
	// the focus/blur churn that a webview emits while you click a button inside it
	// — only hide if focus hasn't returned within a short grace period.
	var focused atomic.Bool
	window.OnWindowEvent(events.Common.WindowFocus, func(*application.WindowEvent) {
		focused.Store(true)
	})
	window.OnWindowEvent(events.Common.WindowLostFocus, func(*application.WindowEvent) {
		focused.Store(false)
		time.AfterFunc(250*time.Millisecond, func() {
			if !focused.Load() {
				window.Hide()
			}
		})
	})

	tray := c.app.SystemTray.New()
	tray.SetIcon(c.icons.Idle)
	tray.AttachWindow(window).WindowOffset(8)

	// Right-click menu as a safety net / quick actions.
	menu := c.app.NewMenu()
	menu.Add("Start server").OnClick(func(*application.Context) { go c.dispatch(proj.ID, "", "startall") })
	menu.Add("Stop server").OnClick(func(*application.Context) { go c.dispatch(proj.ID, "", "stopall") })
	menu.Add("Refresh").OnClick(func(*application.Context) { c.Refresh() })
	menu.AddSeparator()
	menu.Add("Quit einhasad-bar").OnClick(func(*application.Context) { c.app.Quit() })
	tray.SetMenu(menu)

	c.trays[proj.ID] = tray
}

// Refresh builds the current snapshot, pushes it to every popover, and repaints
// the tray icons/labels. Safe to call from the ticker and from action handlers.
func (c *Controller) Refresh() {
	snap := state.Build(c.projects, c.sup)

	if data, err := json.Marshal(snap); err == nil {
		c.app.Event.Emit("eb:state", string(data))
	}

	for _, pv := range snap.Projects {
		tray := c.trays[pv.ID]
		if tray == nil {
			continue
		}
		name, icon := c.iconFor(pv)
		if c.lastIcon[pv.ID] != name {
			tray.SetIcon(icon)
			c.lastIcon[pv.ID] = name
		}
		label := fmt.Sprintf("%s %d/%d", strings.ToUpper(pv.ID), pv.ReqUp, pv.ReqTotal)
		if pv.ReqStarting > 0 {
			label += "…"
		}
		tray.SetLabel(label)
	}
}

func (c *Controller) iconFor(pv state.ProjectView) (string, []byte) {
	switch {
	case pv.ReqStarting > 0:
		return "starting", c.icons.Starting
	case pv.ReqTotal == 0 || pv.ReqUp == pv.ReqTotal:
		return "busy", c.icons.Busy
	case pv.ReqUp == 0:
		return "idle", c.icons.Idle
	default:
		return "mixed", c.icons.Mixed
	}
}

// onAction handles events emitted by the popover frontend. The payload is a JSON
// string {op, project, service} (string on the wire to avoid decode ambiguity).
func (c *Controller) onAction(e *application.CustomEvent) {
	raw, ok := e.Data.(string)
	if !ok {
		return
	}
	var a struct {
		Op      string `json:"op"`
		Project string `json:"project"`
		Service string `json:"service"`
	}
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return
	}
	go c.dispatch(a.Project, a.Service, a.Op)
}

// dispatch performs one action then re-emits state so the popover updates at once
// (e.g. shows blue "starting" immediately, without waiting for the next tick).
func (c *Controller) dispatch(projectID, serviceID, op string) {
	switch op {
	case "quit":
		c.app.Quit()
		return
	case "refresh":
		c.Refresh()
		return
	}

	// Serialise mutating actions per project so a fast Stop→Start (or two
	// popovers) can't race the pidfile.
	unlock := c.lockProject(projectID)
	defer unlock()

	switch op {
	case "startall":
		// "Start server" brings up required services only.
		c.forEachServer(projectID, true, true)
	case "stopall":
		// "Stop server" tears down everything, optional included.
		c.forEachServer(projectID, false, false)
	case "start":
		if svc, ok := c.service(projectID, serviceID); ok {
			c.startService(projectID, svc)
		}
	case "stop":
		if svc, ok := c.service(projectID, serviceID); ok {
			c.stopService(projectID, svc)
		}
	case "openurl":
		if svc, ok := c.service(projectID, serviceID); ok && svc.URL != "" {
			_ = exec.Command("open", svc.URL).Start()
		}
	case "taillog":
		if logPath, err := paths.LogFile(projectID, serviceID); err == nil {
			_ = exec.Command("open", "-a", "Console", logPath).Start()
		}
	}
	c.Refresh()
}

// lockProject returns the project's action mutex, locked; call the returned
// function to unlock.
func (c *Controller) lockProject(projectID string) func() {
	actual, _ := c.locks.LoadOrStore(projectID, &sync.Mutex{})
	m := actual.(*sync.Mutex)
	m.Lock()
	return m.Unlock
}

// startService/stopService route one service to the right backend: process
// services are supervised (pidfile), watch services run their Start/Stop command
// detached/to-completion with health left as the source of truth.
func (c *Controller) startService(projectID string, svc config.Service) {
	if svc.Mode == config.ModeWatch {
		if svc.Start != nil {
			_ = c.sup.RunLifecycle(projectID, svc, svc.Start, true)
		}
		return
	}
	_ = c.sup.Start(projectID, svc)
}

func (c *Controller) stopService(projectID string, svc config.Service) {
	if svc.Mode == config.ModeWatch {
		if svc.Stop != nil {
			_ = c.sup.RunLifecycle(projectID, svc, svc.Stop, false)
		}
		return
	}
	_ = c.sup.Stop(projectID, svc)
}

// forEachServer starts or stops a project's controllable services, deduplicating
// by Group so a shared delegate command (EV's `dev/local/server up`) runs once.
// requiredOnly limits to required services (used by "Start server").
func (c *Controller) forEachServer(projectID string, requiredOnly, start bool) {
	for _, proj := range c.projects {
		if proj.ID != projectID {
			continue
		}
		seen := map[string]bool{}
		for _, svc := range proj.Services {
			if !isToggleable(svc) {
				continue
			}
			if requiredOnly && !svc.IsRequired() {
				continue
			}
			if svc.Group != "" {
				if seen[svc.Group] {
					continue
				}
				seen[svc.Group] = true
			}
			if start {
				c.startService(projectID, svc)
			} else {
				c.stopService(projectID, svc)
			}
		}
	}
}

// isToggleable reports whether a service has Start/Stop controls: process
// services always do; watch services only when they declare a lifecycle command.
func isToggleable(svc config.Service) bool {
	if svc.Mode == config.ModeProcess {
		return true
	}
	return svc.Start != nil || svc.Stop != nil
}

func (c *Controller) service(projectID, serviceID string) (config.Service, bool) {
	for _, proj := range c.projects {
		if proj.ID != projectID {
			continue
		}
		for _, svc := range proj.Services {
			if svc.ID == serviceID {
				return svc, true
			}
		}
	}
	return config.Service{}, false
}
