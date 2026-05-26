//go:build linux

package ui

import "os/exec"

// openURL opens a URL in the default browser via the freedesktop opener.
func openURL(url string) { _ = exec.Command("xdg-open", url).Start() }

// openLog opens a service log file in the default text/log viewer.
func openLog(path string) { _ = exec.Command("xdg-open", path).Start() }
