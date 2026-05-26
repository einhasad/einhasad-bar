//go:build darwin

package ui

import "os/exec"

// openURL opens a URL in the default browser.
func openURL(url string) { _ = exec.Command("open", url).Start() }

// openLog opens a service log file in Console.app.
func openLog(path string) { _ = exec.Command("open", "-a", "Console", path).Start() }
