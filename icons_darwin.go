//go:build darwin

package main

import _ "embed"

//go:embed assets/icons/idle.pdf
var iconIdle []byte

//go:embed assets/icons/busy.pdf
var iconBusy []byte

//go:embed assets/icons/mixed.pdf
var iconMixed []byte

//go:embed assets/icons/starting.pdf
var iconStarting []byte
