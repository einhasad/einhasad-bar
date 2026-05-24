package supervisor_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/einhasad/einhasad-bar/internal/config"
	"github.com/einhasad/einhasad-bar/internal/paths"
	"github.com/einhasad/einhasad-bar/internal/supervisor"
)

// svc is a long-running process service used to exercise the lifecycle.
func svc() config.Service {
	return config.Service{
		ID:      "sleeper",
		Label:   "Sleeper",
		Mode:    config.ModeProcess,
		Command: "sh",
		Args:    []string{"-c", "sleep 60"},
	}
}

func TestStartStopLifecycle(t *testing.T) {
	// Arrange — a unique project id keeps state out of real stacks; clean it up.
	projID := "ebtest-lifecycle"
	t.Cleanup(func() {
		dir, _ := paths.StateDir(projID)
		os.RemoveAll(dir)
		ldir, _ := paths.LogDir(projID)
		os.RemoveAll(ldir)
	})
	sup := supervisor.New()
	service := svc()

	// Act — start the service.
	err := sup.Start(projID, service)

	// Assert — it is tracked, alive, and a pidfile + log file exist.
	assertNil(t, "start succeeds", err)
	assertTrue(t, "service reports alive", sup.Alive(projID, service.ID))
	_, ok := sup.PID(projID, service.ID)
	assertTrue(t, "pid is known", ok)
	pidPath, _ := paths.PidFile(projID, service.ID)
	assertTrue(t, "pidfile exists", fileExists(pidPath))
	logPath, _ := paths.LogFile(projID, service.ID)
	assertTrue(t, "log file exists", fileExists(logPath))

	// Act — stop the service.
	err = sup.Stop(projID, service)

	// Assert — process is gone and the pidfile was removed.
	assertNil(t, "stop succeeds", err)
	assertFalse(t, "service no longer alive", sup.Alive(projID, service.ID))
	assertFalse(t, "pidfile removed", fileExists(pidPath))
}

func TestStartIsIdempotent(t *testing.T) {
	// Arrange
	projID := "ebtest-idempotent"
	t.Cleanup(func() {
		dir, _ := paths.StateDir(projID)
		os.RemoveAll(dir)
		ldir, _ := paths.LogDir(projID)
		os.RemoveAll(ldir)
	})
	sup := supervisor.New()
	service := svc()

	// Act — start twice; the second call should be a no-op on the same pid.
	assertNil(t, "first start", sup.Start(projID, service))
	pid1, _ := sup.PID(projID, service.ID)
	assertNil(t, "second start", sup.Start(projID, service))
	pid2, _ := sup.PID(projID, service.ID)

	// Assert
	eqInt(t, "pid unchanged across repeated start", pid1, pid2)

	// Cleanup the running process.
	_ = sup.Stop(projID, service)
	// give the reaper a moment
	time.Sleep(100 * time.Millisecond)
}

func TestRunLifecycle_runsStopCommandToCompletion(t *testing.T) {
	// Arrange — a watch service whose "stop" command writes a marker to its log.
	projID := "ebtest-lifecycle-watch"
	t.Cleanup(func() {
		d, _ := paths.StateDir(projID)
		os.RemoveAll(d)
		l, _ := paths.LogDir(projID)
		os.RemoveAll(l)
	})
	sup := supervisor.New()
	service := config.Service{ID: "watcher", Label: "Watcher", Mode: config.ModeWatch}
	stop := &config.Command{Command: "sh", Args: []string{"-c", "echo stopped-marker"}}

	// Act — detach=false runs to completion before returning.
	err := sup.RunLifecycle(projID, service, stop, false)

	// Assert — no error, no pidfile (watch never tracks a pid), output logged.
	assertNil(t, "lifecycle runs", err)
	pidPath, _ := paths.PidFile(projID, service.ID)
	assertFalse(t, "no pidfile written for watch lifecycle", fileExists(pidPath))
	logPath, _ := paths.LogFile(projID, service.ID)
	data, _ := os.ReadFile(logPath)
	assertTrue(t, "command output is logged", strings.Contains(string(data), "stopped-marker"))
}

func TestRunLifecycle_rejectsEmptyCommand(t *testing.T) {
	// Arrange
	sup := supervisor.New()
	service := config.Service{ID: "watcher", Mode: config.ModeWatch}

	// Act — a nil command can't run.
	err := sup.RunLifecycle("ebtest-empty", service, nil, false)

	// Assert
	assertTrue(t, "nil command errors", err != nil)
}

// --- assert helpers (branching kept out of test bodies) ---

func assertNil(t *testing.T, msg string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", msg, err)
	}
}

func assertTrue(t *testing.T, msg string, got bool) {
	t.Helper()
	if !got {
		t.Fatalf("%s: got false, want true", msg)
	}
}

func assertFalse(t *testing.T, msg string, got bool) {
	t.Helper()
	if got {
		t.Fatalf("%s: got true, want false", msg)
	}
}

func eqInt(t *testing.T, msg string, got, want int) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %d, want %d", msg, got, want)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
