//go:build linux

package paths

import (
	"os"
	"path/filepath"
)

// cacheRoot follows the XDG Base Directory spec: $XDG_CACHE_HOME or ~/.cache.
func cacheRoot() string {
	if dir := os.Getenv("XDG_CACHE_HOME"); dir != "" {
		return filepath.Join(dir, appName)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", appName)
}

// logRoot uses $XDG_STATE_HOME or ~/.local/state (logs are state, not cache).
func logRoot() string {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, appName, "logs")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", appName, "logs")
}
