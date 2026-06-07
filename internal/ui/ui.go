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
	app             *application.App
	sup             *supervisor.Supervisor
	icons           Icons
	mu              sync.RWMutex
	projects        []config.Project
	trays           map[string]*application.SystemTray
	windows         map[string]*application.WebviewWindow
	lastIcon        map[string]string
	locks           sync.Map // projectID → *sync.Mutex, serialises actions per project
	actionsRunning  sync.Map // projectID → action label (string)
	actionsResult   sync.Map // projectID → actionResult, cleared after 3s
	servicesRunning sync.Map // "projectID/serviceID" → "starting"|"stopping"
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
		windows:  map[string]*application.WebviewWindow{},
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
				// Keep the popover open while a project action is running so the
				// user can see the live state updates.
				if _, running := c.actionsRunning.Load(proj.ID); !running {
					window.Hide()
				}
			}
		})
	})

	tray := c.app.SystemTray.New()
	tray.SetIcon(c.icons.Idle)
	tray.AttachWindow(window).WindowOffset(8)

	// Right-click menu: project-level actions from YAML, then Refresh / Quit.
	menu := c.app.NewMenu()
	for _, action := range proj.Actions {
		action := action
		menu.Add(action.Label).OnClick(func(*application.Context) { go c.runAction(proj.ID, action) })
	}
	if len(proj.Actions) > 0 {
		menu.AddSeparator()
	}
	menu.Add("Refresh").OnClick(func(*application.Context) { c.Refresh() })
	menu.AddSeparator()
	menu.Add("Quit").OnClick(func(*application.Context) { go c.quitProject(proj.ID) })
	tray.SetMenu(menu)

	c.trays[proj.ID] = tray
	c.windows[proj.ID] = window
}

// Refresh builds the current snapshot, pushes it to every popover, and repaints
// the tray icons/labels. Safe to call from the ticker and from action handlers.
func (c *Controller) Refresh() {
	c.mu.RLock()
	projects := append([]config.Project(nil), c.projects...)
	c.mu.RUnlock()

	snap := state.Build(projects, c.sup)

	// Annotate projects and services where an action is currently running.
	for i := range snap.Projects {
		pid := snap.Projects[i].ID
		var projAction string
		if v, ok := c.actionsRunning.Load(pid); ok {
			projAction = v.(string)
			snap.Projects[i].ActionRunning = projAction
		}
		if v, ok := c.actionsResult.Load(pid); ok {
			r := v.(actionResult)
			snap.Projects[i].ActionResult = r.label
			snap.Projects[i].ActionResultOK = r.ok
		}
		for j := range snap.Projects[i].Services {
			key := pid + "/" + snap.Projects[i].Services[j].ID
			if v, ok := c.servicesRunning.Load(key); ok {
				snap.Projects[i].Services[j].ActionRunning = v.(string)
			}
		}
	}

	if data, err := json.Marshal(snap); err == nil {
		c.app.Event.Emit("eb:state", string(data))
	}

	for _, pv := range snap.Projects {
		c.mu.RLock()
		tray := c.trays[pv.ID]
		lastName := c.lastIcon[pv.ID]
		c.mu.RUnlock()

		if tray == nil {
			continue
		}
		var name string
		var icon []byte
		if pv.ActionRunning != "" {
			name = "action:" + pv.ActionRunning
			icon = c.icons.Starting
		} else {
			name, icon = c.iconFor(pv)
		}
		if lastName != name {
			c.mu.Lock()
			c.lastIcon[pv.ID] = name
			c.mu.Unlock()
			tray.SetIcon(icon)
		}
		var label string
		if pv.ActionRunning != "" {
			label = strings.ToUpper(pv.ID) + "…"
		} else if pv.ReqTotal > 0 && pv.ReqUp < pv.ReqTotal {
			label = fmt.Sprintf("%s %d/%d", strings.ToUpper(pv.ID), pv.ReqUp, pv.ReqTotal)
			if pv.ReqStarting > 0 {
				label += "…"
			}
		} else {
			label = strings.ToUpper(pv.ID)
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
	// Mark the action as running synchronously — before the goroutine starts —
	// so the 250ms window-hide guard in addProject sees it immediately and keeps
	// the popover open while the action runs.
	if a.Op == "action" && a.Project != "" && a.Service != "" {
		c.actionsRunning.Store(a.Project, a.Service)
		c.Refresh()
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
	case "action":
		c.dispatchAction(projectID, serviceID)
		return
	}

	// Show immediate in-progress feedback for start/stop before acquiring the
	// project lock (which may block up to 8s waiting for a process to die).
	if op == "start" || op == "stop" {
		svcKey := projectID + "/" + serviceID
		label := "starting"
		if op == "stop" {
			label = "stopping"
		}
		c.servicesRunning.Store(svcKey, label)
		c.Refresh()
		defer c.servicesRunning.Delete(svcKey)
	}

	// Serialise mutating actions per project so a fast Stop→Start (or two
	// popovers) can't race the pidfile.
	unlock := c.lockProject(projectID)
	defer unlock()

	switch op {
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
			openURL(svc.URL)
		}
	case "taillog":
		if logPath, err := paths.LogFile(projectID, serviceID); err == nil {
			openLog(logPath)
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

func (c *Controller) startService(projectID string, svc config.Service) {
	if svc.Mode == config.ModeProcess {
		_ = c.sup.Start(projectID, svc)
	}
}

func (c *Controller) stopService(projectID string, svc config.Service) {
	if svc.Mode == config.ModeProcess {
		_ = c.sup.Stop(projectID, svc)
	}
}

func (c *Controller) dispatchAction(projectID, label string) {
	c.mu.RLock()
	projects := append([]config.Project(nil), c.projects...)
	c.mu.RUnlock()
	for _, proj := range projects {
		if proj.ID != projectID {
			continue
		}
		for _, a := range proj.Actions {
			if a.Label == label {
				c.runAction(projectID, a)
				return
			}
		}
	}
}

type actionResult struct {
	label string
	ok    bool
}

// runAction runs a project-level action command to completion and refreshes.
// It sets actionsRunning for the project so the tray icon and popover show a
// live "in progress" state immediately, rather than waiting for the command to
// finish (which can take 10+ seconds for server up/down).
func (c *Controller) runAction(projectID string, action config.Action) {
	c.actionsRunning.Store(projectID, action.Label)

	// Force tray icon to Starting immediately, bypassing the lastIcon guard.
	c.mu.RLock()
	tray := c.trays[projectID]
	c.mu.RUnlock()
	if tray != nil {
		iconName := "action:" + action.Label
		c.mu.Lock()
		c.lastIcon[projectID] = iconName
		c.mu.Unlock()
		tray.SetIcon(c.icons.Starting)
		tray.SetLabel(strings.ToUpper(projectID) + "…")
	}

	c.Refresh()

	err := action.Cmd().Run()

	c.actionsRunning.Delete(projectID)
	c.actionsResult.Store(projectID, actionResult{label: action.Label, ok: err == nil})
	c.Refresh()

	go func() {
		time.Sleep(3 * time.Second)
		c.actionsResult.Delete(projectID)
		c.Refresh()
	}()
}

// quitProject stops all process-mode services for one project, removes its tray
// icon and popover, and quits the app only if no projects remain.
func (c *Controller) quitProject(projectID string) {
	unlock := c.lockProject(projectID)
	c.forEachServer(projectID, false, false)
	unlock()

	c.mu.Lock()
	tray := c.trays[projectID]
	window := c.windows[projectID]
	delete(c.trays, projectID)
	delete(c.windows, projectID)
	delete(c.lastIcon, projectID)
	for i, p := range c.projects {
		if p.ID == projectID {
			c.projects = append(c.projects[:i], c.projects[i+1:]...)
			break
		}
	}
	remaining := len(c.trays)
	c.mu.Unlock()

	if window != nil {
		window.Close()
	}
	if tray != nil {
		tray.Destroy()
	}
	if remaining == 0 {
		c.app.Quit()
	}
}

// forEachServer starts or stops a project's controllable services, deduplicating
// by Group so a shared delegate command (EV's `dev/local/server up`) runs once.
// requiredOnly limits to required services (used by "Start server").
func (c *Controller) forEachServer(projectID string, requiredOnly, start bool) {
	c.mu.RLock()
	projects := append([]config.Project(nil), c.projects...)
	c.mu.RUnlock()

	for _, proj := range projects {
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
	c.mu.RLock()
	defer c.mu.RUnlock()
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
