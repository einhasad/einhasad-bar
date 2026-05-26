//go:build linux

package main

import _ "embed"

//go:embed assets/icons/idle.png
var iconIdle []byte

//go:embed assets/icons/busy.png
var iconBusy []byte

//go:embed assets/icons/mixed.png
var iconMixed []byte

//go:embed assets/icons/starting.png
var iconStarting []byte
