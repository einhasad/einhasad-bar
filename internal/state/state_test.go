package state_test

import (
	"net"
	"os"
	"strconv"
	"testing"

	"github.com/einhasad/einhasad-bar/internal/config"
	"github.com/einhasad/einhasad-bar/internal/paths"
	"github.com/einhasad/einhasad-bar/internal/state"
	"github.com/einhasad/einhasad-bar/internal/supervisor"
)

// openPort returns a port with a live listener (closed when the test ends).
func openPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	_, p, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(p)
	return port
}

func TestBuild_watchStates(t *testing.T) {
	// Arrange — one watch service on a live port (up), one on a closed port (down).
	live := openPort(t)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	_, dp, _ := net.SplitHostPort(ln.Addr().String())
	deadClosed, _ := strconv.Atoi(dp)
	ln.Close()

	proj := config.Project{ID: "twatch", Name: "Watch", Services: []config.Service{
		{ID: "live", Label: "Live", Mode: config.ModeWatch, Check: &config.Check{Type: config.CheckTCP, Port: live}},
		{ID: "dead", Label: "Dead", Mode: config.ModeWatch, Check: &config.Check{Type: config.CheckTCP, Port: deadClosed}},
	}}

	// Act
	snap := state.Build([]config.Project{proj}, supervisor.New())

	// Assert
	eqInt(t, "one project", len(snap.Projects), 1)
	eqStr(t, "live watch is up", snap.Projects[0].Services[0].State, state.StateUp)
	eqStr(t, "dead watch is down", snap.Projects[0].Services[1].State, state.StateDown)
	eqInt(t, "required up count", snap.Projects[0].ReqUp, 1)
}

func TestBuild_watchIsMonitorOnly(t *testing.T) {
	// Arrange — a live watch service. Watch services are monitor-only: their
	// state mirrors the health check and they never expose Start/Stop controls.
	live := openPort(t)
	proj := config.Project{ID: "twatch2", Name: "Watch", Services: []config.Service{
		{ID: "mon", Label: "MonitorOnly", Mode: config.ModeWatch,
			Check: &config.Check{Type: config.CheckTCP, Port: live}},
	}}

	// Act
	snap := state.Build([]config.Project{proj}, supervisor.New())

	// Assert — up via health, running mirrors the check, but never toggleable.
	svc := snap.Projects[0].Services[0]
	eqStr(t, "watch is up", svc.State, state.StateUp)
	eqBool(t, "watch running mirrors check", svc.Running, true)
	eqBool(t, "watch does not toggle", svc.CanToggle, false)
}

func TestBuild_processStartingThenDown(t *testing.T) {
	// Arrange — a process service with a tcp check pointed at a CLOSED port, plus
	// a long-running sleeper as the managed process. Alive + check failing must
	// resolve to the blue "starting" state.
	projID := "tproc-start"
	t.Cleanup(func() {
		d, _ := paths.StateDir(projID)
		os.RemoveAll(d)
		l, _ := paths.LogDir(projID)
		os.RemoveAll(l)
	})
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	_, p, _ := net.SplitHostPort(ln.Addr().String())
	closedPort, _ := strconv.Atoi(p)
	ln.Close() // ensure the readiness check never passes

	svc := config.Service{
		ID: "sleeper", Label: "Sleeper", Mode: config.ModeProcess,
		Command: "sh", Args: []string{"-c", "sleep 60"},
		Check: &config.Check{Type: config.CheckTCP, Port: closedPort},
	}
	proj := config.Project{ID: projID, Name: "Proc", Services: []config.Service{svc}}
	sup := supervisor.New()

	// Act 1 — before start: down.
	before := state.Build([]config.Project{proj}, sup)
	eqStr(t, "process not started is down", before.Projects[0].Services[0].State, state.StateDown)

	// Act 2 — after start: alive but check failing → starting (blue).
	if err := sup.Start(projID, svc); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = sup.Stop(projID, svc) })
	starting := state.Build([]config.Project{proj}, sup)

	// Assert
	sv := starting.Projects[0].Services[0]
	eqStr(t, "alive + check-failing is starting", sv.State, state.StateStarting)
	eqBool(t, "running flag true", sv.Running, true)
	eqBool(t, "canToggle true for process", sv.CanToggle, true)
	eqInt(t, "counts it as starting", starting.Projects[0].ReqStarting, 1)
}

// --- assert helpers (branching kept out of test bodies) ---

func eqStr(t *testing.T, msg, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %q, want %q", msg, got, want)
	}
}

func eqInt(t *testing.T, msg string, got, want int) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %d, want %d", msg, got, want)
	}
}

func eqBool(t *testing.T, msg string, got, want bool) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %v, want %v", msg, got, want)
	}
}
