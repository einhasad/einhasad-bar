//go:build darwin

package paths

import (
	"path/filepath"
	"testing"
)

func TestRootsDarwin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if got, want := cacheRoot(), filepath.Join(home, "Library", "Caches", appName); got != want {
		t.Errorf("cacheRoot = %q, want %q", got, want)
	}
	if got, want := logRoot(), filepath.Join(home, "Library", "Logs", appName); got != want {
		t.Errorf("logRoot = %q, want %q", got, want)
	}
}
