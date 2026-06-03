// Package state builds a JSON-serialisable snapshot of every project's services
// for the reactive webview popover. It is pure (no UI), so it's easy to test and
// reuse: the UI layer just marshals a Snapshot and emits it to the frontend.
//
// Each service resolves to one of three states — "up" (health-check passing),
// "starting" (process alive but check not passing yet → blue), or "down".
package state

import (
	"fmt"

	"github.com/einhasad/einhasad-bar/internal/config"
	"github.com/einhasad/einhasad-bar/internal/health"
	"github.com/einhasad/einhasad-bar/internal/stats"
	"github.com/einhasad/einhasad-bar/internal/supervisor"
)

// Service states as sent to the frontend.
const (
	StateUp       = "up"
	StateStarting = "starting"
	StateDown     = "down"
)

// ServiceView is one service's state on the wire.
type ServiceView struct {
	ID            string `json:"id"`
	Label         string `json:"label"`
	Mode          string `json:"mode"`
	State         string `json:"state"`         // up | starting | down
	Info          string `json:"info"`          // stats line or status text
	URL           string `json:"url"`           // empty if none
	Required      bool   `json:"required"`      // gates the icon colour
	Running       bool   `json:"running"`       // managed process alive (drives Start/Stop)
	CanToggle     bool   `json:"canToggle"`     // process mode → show Start/Stop
	ActionRunning string `json:"actionRunning"` // "starting" | "stopping" | ""
}

// ActionView is one project-level action as sent to the frontend.
type ActionView struct {
	Label string `json:"label"`
}

// ProjectView is one project's state on the wire.
type ProjectView struct {
	ID              string        `json:"id"`
	Name            string        `json:"name"`
	Actions         []ActionView  `json:"actions"`
	Services        []ServiceView `json:"services"`
	ReqUp           int           `json:"reqUp"`
	ReqTotal        int           `json:"reqTotal"`
	ReqStarting     int           `json:"reqStarting"`
	ActionRunning   string        `json:"actionRunning"`   // label of the running action, or ""
	ActionResult    string        `json:"actionResult"`    // label of the last completed action, or ""
	ActionResultOK  bool          `json:"actionResultOk"`  // true=exit 0, false=error
}

// Snapshot is the full payload emitted to every popover.
type Snapshot struct {
	Projects []ProjectView `json:"projects"`
}

// Build computes the current snapshot across all projects.
func Build(projects []config.Project, sup *supervisor.Supervisor) Snapshot {
	snap := Snapshot{}
	for _, proj := range projects {
		pv := ProjectView{ID: proj.ID, Name: proj.Name}
		for _, a := range proj.Actions {
			pv.Actions = append(pv.Actions, ActionView{Label: a.Label})
		}
		for _, svc := range proj.Services {
			sv := buildService(proj.ID, svc, sup)
			pv.Services = append(pv.Services, sv)
			if sv.Required {
				pv.ReqTotal++
				switch sv.State {
				case StateUp:
					pv.ReqUp++
				case StateStarting:
					pv.ReqStarting++
				}
			}
		}
		snap.Projects = append(snap.Projects, pv)
	}
	return snap
}

func buildService(projID string, svc config.Service, sup *supervisor.Supervisor) ServiceView {
	sv := ServiceView{
		ID:        svc.ID,
		Label:     svc.Label,
		Mode:      string(svc.Mode),
		URL:       svc.URL,
		Required:  svc.IsRequired(),
		CanToggle: svc.Mode == config.ModeProcess,
	}

	if svc.Mode == config.ModeProcess {
		running := sup.Alive(projID, svc.ID)
		sv.Running = running
		hasCheck := svc.Check != nil && !(svc.Check.Type == config.CheckPidfile && svc.Check.Pidfile == "")

		switch {
		case hasCheck && health.Probe(svc.Check):
			sv.State = StateUp
		case !hasCheck && running:
			sv.State = StateUp
		case running:
			sv.State = StateStarting
		default:
			sv.State = StateDown
		}

		switch sv.State {
		case StateUp:
			pid, _ := sup.PID(projID, svc.ID)
			t := stats.Collect(pid)
			sv.Info = fmt.Sprintf("running · %dp  %.0f%%  %.0fM  pid=%d", t.Procs, t.CPU, t.RSSMB, pid)
		case StateStarting:
			sv.Info = "starting… waiting for " + health.String(svc.Check)
		default:
			sv.Info = "stopped"
		}
		return sv
	}

	// watch: status is purely the check result; no lifecycle control.
	if health.Probe(svc.Check) {
		sv.State = StateUp
		sv.Running = true
		sv.Info = "running (watched)"
	} else {
		sv.State = StateDown
		sv.Info = "down (watched)"
	}
	return sv
}
