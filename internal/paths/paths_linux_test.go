//go:build linux

package paths

import (
	"path/filepath"
	"testing"
)

func TestCacheRootLinux(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Fallback when XDG_CACHE_HOME is unset/empty.
	t.Setenv("XDG_CACHE_HOME", "")
	if got, want := cacheRoot(), filepath.Join(home, ".cache", appName); got != want {
		t.Errorf("cacheRoot fallback = %q, want %q", got, want)
	}

	// Honour XDG_CACHE_HOME when set.
	xdg := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", xdg)
	if got, want := cacheRoot(), filepath.Join(xdg, appName); got != want {
		t.Errorf("cacheRoot with XDG_CACHE_HOME = %q, want %q", got, want)
	}
}

func TestLogRootLinux(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Fallback when XDG_STATE_HOME is unset/empty.
	t.Setenv("XDG_STATE_HOME", "")
	if got, want := logRoot(), filepath.Join(home, ".local", "state", appName, "logs"); got != want {
		t.Errorf("logRoot fallback = %q, want %q", got, want)
	}

	// Honour XDG_STATE_HOME when set.
	xdg := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdg)
	if got, want := logRoot(), filepath.Join(xdg, appName, "logs"); got != want {
		t.Errorf("logRoot with XDG_STATE_HOME = %q, want %q", got, want)
	}
}
