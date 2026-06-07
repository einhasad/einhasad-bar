package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/einhasad/einhasad-bar/internal/config"
	"github.com/einhasad/einhasad-bar/internal/paths"
	"github.com/einhasad/einhasad-bar/internal/supervisor"
)

// TestRunStartStop exercises the headless start/stop commands end-to-end against
// a temp project with one process service (a long sleep) and one watch service:
// start must bring the process service up (alive + pidfile) and skip the watch
// one; stop must tear it back down.
func TestRunStartStop(t *testing.T) {
	// Arrange — a unique project id keeps state out of real stacks; clean it up.
	projID := "ebtest-cli-startstop"
	t.Cleanup(func() {
		d, _ := paths.StateDir(projID)
		os.RemoveAll(d)
		l, _ := paths.LogDir(projID)
		os.RemoveAll(l)
	})
	cfg := filepath.Join(t.TempDir(), "einhasad-bar.yaml")
	yaml := "project:\n  id: " + projID + "\nservices:\n" +
		"  - id: sleeper\n    mode: process\n    command: sh\n    args: [\"-c\", \"sleep 60\"]\n" +
		"  - id: db\n    mode: watch\n    check: { type: tcp, port: 1 }\n"
	if err := os.WriteFile(cfg, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	sup := supervisor.New()
	pidPath, _ := paths.PidFile(projID, "sleeper")

	// Act — start (no names ⇒ required process services only).
	if err := runStart([]string{"-f", cfg}); err != nil {
		t.Fatalf("runStart: %v", err)
	}

	// Assert — process service is alive + tracked; watch service is untouched.
	if !sup.Alive(projID, "sleeper") {
		t.Fatal("sleeper should be alive after start")
	}
	if _, err := os.Stat(pidPath); err != nil {
		t.Fatalf("pidfile should exist after start: %v", err)
	}
	if sup.Alive(projID, "db") {
		t.Fatal("watch service should never be started")
	}

	// Act — stop (no names ⇒ all process services).
	if err := runStop([]string{"-f", cfg}); err != nil {
		t.Fatalf("runStop: %v", err)
	}

	// Assert — process gone and pidfile removed.
	if sup.Alive(projID, "sleeper") {
		t.Fatal("sleeper should be stopped")
	}
	if _, err := os.Stat(pidPath); err == nil {
		t.Fatal("pidfile should be removed after stop")
	}
}

// TestSelectProcessServices covers the selection rules: required-only default,
// explicit names, watch services skipped, and unknown names rejected.
func TestSelectProcessServices(t *testing.T) {
	required := true
	optional := false
	proj := config.Project{
		ID: "x",
		Services: []config.Service{
			{ID: "garage", Mode: config.ModeProcess, Command: "true", Required: &required},
			{ID: "e2e", Mode: config.ModeProcess, Command: "true", Required: &optional},
			{ID: "db", Mode: config.ModeWatch, Check: &config.Check{Type: config.CheckTCP, Port: 1}},
		},
	}

	// No names + requiredOnly ⇒ only the required process service.
	targets, _, err := selectProcessServices(proj, nil, true)
	if err != nil {
		t.Fatalf("select required: %v", err)
	}
	if len(targets) != 1 || targets[0].ID != "garage" {
		t.Fatalf("required-only should pick [garage], got %v", ids(targets))
	}

	// No names + not requiredOnly ⇒ all process services (skips watch).
	targets, _, _ = selectProcessServices(proj, nil, false)
	if len(targets) != 2 {
		t.Fatalf("all process services expected (garage,e2e), got %v", ids(targets))
	}

	// Explicit watch name ⇒ reported as skipped, not a target.
	targets, skipped, err := selectProcessServices(proj, []string{"db"}, false)
	if err != nil {
		t.Fatalf("select db: %v", err)
	}
	if len(targets) != 0 || len(skipped) != 1 || skipped[0].ID != "db" {
		t.Fatalf("watch name should be skipped, got targets=%v skipped=%v", ids(targets), ids(skipped))
	}

	// Unknown name ⇒ error.
	if _, _, err := selectProcessServices(proj, []string{"nope"}, false); err == nil {
		t.Fatal("unknown service name should error")
	}
}

// TestRunForeground_guards covers the foreground arg validation (these all return
// before spawning anything), plus a happy-path run of a fast command (`true`).
func TestRunForeground_guards(t *testing.T) {
	proj := config.Project{
		ID: "ebtest-fg",
		Services: []config.Service{
			{ID: "api", Label: "API", Mode: config.ModeProcess, Command: "true"},
			{ID: "db", Label: "DB", Mode: config.ModeWatch, Check: &config.Check{Type: config.CheckTCP, Port: 1}},
		},
	}

	if err := runForeground(proj, nil); err == nil {
		t.Fatal("no service name should error")
	}
	if err := runForeground(proj, []string{"api", "db"}); err == nil {
		t.Fatal("multiple services should error")
	}
	if err := runForeground(proj, []string{"nope"}); err == nil {
		t.Fatal("unknown service should error")
	}
	if err := runForeground(proj, []string{"db"}); err == nil {
		t.Fatal("watch service should error")
	}
	// Happy path: `true` exits immediately, so RunForeground returns without blocking.
	if err := runForeground(proj, []string{"api"}); err != nil {
		t.Fatalf("foreground run of `true` should succeed: %v", err)
	}
}

func ids(svcs []config.Service) []string {
	out := make([]string, len(svcs))
	for i, s := range svcs {
		out[i] = s.ID
	}
	return out
}
