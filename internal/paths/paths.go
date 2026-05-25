// Package paths centralises einhasad-bar's per-project state and log locations,
// mirroring the SwiftBar setup it replaces:
//
//	state/pidfiles → ~/Library/Caches/einhasad-bar/<project>/
//	logs           → ~/Library/Logs/einhasad-bar/<project>/
package paths

import (
	"os"
	"path/filepath"
)

const appName = "einhasad-bar"

func cacheRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Caches", appName)
}

func logRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Logs", appName)
}

// ConfigDir returns (and creates) the directory holding registered project
// configs. Each registration is a symlink named "<id>.einhasad-bar.yaml" so the
// app's *.einhasad-bar.yaml discovery glob picks it up.
func ConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".config", appName)
	return dir, os.MkdirAll(dir, 0o755)
}

// StateDir returns (and creates) the pidfile/state directory for a project.
func StateDir(projectID string) (string, error) {
	dir := filepath.Join(cacheRoot(), projectID)
	return dir, os.MkdirAll(dir, 0o755)
}

// LogDir returns (and creates) the log directory for a project.
func LogDir(projectID string) (string, error) {
	dir := filepath.Join(logRoot(), projectID)
	return dir, os.MkdirAll(dir, 0o755)
}

// PidFile returns the pidfile path for a managed service.
func PidFile(projectID, serviceID string) (string, error) {
	dir, err := StateDir(projectID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, serviceID+".pid"), nil
}

// LogFile returns the log path for a service.
func LogFile(projectID, serviceID string) (string, error) {
	dir, err := LogDir(projectID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, serviceID+".log"), nil
}

// AppPidFile returns the path where the einhasad-bar process managing a
// project stores its PID. Used by "up" (write) and "down" (signal).
func AppPidFile(projectID string) (string, error) {
	dir, err := StateDir(projectID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "einhasad-bar.pid"), nil
}
