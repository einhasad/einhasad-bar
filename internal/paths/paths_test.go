package paths

import (
	"os"
	"path/filepath"
	"testing"
)

// isolateHome points HOME at a temp dir and clears XDG overrides so every path
// root resolves under the temp dir — no test writes escape into the real
// ~/Library or ~/.cache. Returned dirs are removed automatically by t.TempDir.
func isolateHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
}

func TestStateDir(t *testing.T) {
	isolateHome(t)
	got, err := StateDir("proj")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(cacheRoot(), "proj"); got != want {
		t.Fatalf("StateDir = %q, want %q", got, want)
	}
	if fi, err := os.Stat(got); err != nil || !fi.IsDir() {
		t.Fatalf("StateDir was not created: stat err=%v", err)
	}
}

func TestLogDir(t *testing.T) {
	isolateHome(t)
	got, err := LogDir("proj")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(logRoot(), "proj"); got != want {
		t.Fatalf("LogDir = %q, want %q", got, want)
	}
	if fi, err := os.Stat(got); err != nil || !fi.IsDir() {
		t.Fatalf("LogDir was not created: stat err=%v", err)
	}
}

func TestConfigDir(t *testing.T) {
	isolateHome(t)
	got, err := ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if want := filepath.Join(home, ".config", appName); got != want {
		t.Fatalf("ConfigDir = %q, want %q", got, want)
	}
	if fi, err := os.Stat(got); err != nil || !fi.IsDir() {
		t.Fatalf("ConfigDir was not created: stat err=%v", err)
	}
}

func TestFilePaths(t *testing.T) {
	isolateHome(t)

	pid, err := PidFile("aw", "api")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(cacheRoot(), "aw", "api.pid"); pid != want {
		t.Errorf("PidFile = %q, want %q", pid, want)
	}

	log, err := LogFile("aw", "api")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(logRoot(), "aw", "api.log"); log != want {
		t.Errorf("LogFile = %q, want %q", log, want)
	}

	app, err := AppPidFile("aw")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(cacheRoot(), "aw", "einhasad-bar.pid"); app != want {
		t.Errorf("AppPidFile = %q, want %q", app, want)
	}

	// Pidfiles and the app lock share the state dir but never the same file.
	if pid == app {
		t.Error("service pidfile and app pidfile collide")
	}
}
