//go:build darwin

package paths

import (
	"os"
	"path/filepath"
)

func cacheRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Caches", appName)
}

func logRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Logs", appName)
}
