# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A macOS menu-bar app that manages local dev stacks declared in `*.einhasad-bar.yaml` files. Built with Go + [Wails v3](https://v3.wails.io) (webview popover, no dock icon). One YAML file → one tray icon with live health polling and Start/Stop/Log controls.

## Build & run

```bash
# Build and run locally (requires Go 1.25+, Xcode CLI tools, CGO enabled)
go build -o einhasad-bar . && ./einhasad-bar

# Run all tests
go test ./...

# Run tests for a single package
go test ./internal/supervisor/...

# GoReleaser local snapshot build (arm64 only)
goreleaser build --single-target --snapshot --clean
```

The release pipeline is CI-only: push a `vX.Y.Z` tag → `.github/workflows/release.yml` → GoReleaser builds darwin/arm64, cuts a GitHub release, updates `einhasad/homebrew-tap`.

## Architecture

```
main.go            CLI dispatch (register/unregister/list) + Wails app bootstrap
internal/
  config/          YAML parsing & validation → Project/Service structs
  paths/           macOS paths: ~/.config/einhasad-bar/ (symlinks), ~/Library/{Caches,Logs}/einhasad-bar/
  supervisor/      Process lifecycle: spawn in own pgid, pidfile, SIGTERM→SIGKILL, RunLifecycle for watch cmds
  health/          Stateless probes: tcp (dial port), http (2xx/3xx), pidfile (kill -0)
  stats/           CPU/RSS collection for running processes (gopsutil)
  state/           Builds a JSON Snapshot (ProjectView/ServiceView) from config + supervisor + health
  ui/              Wails Controller: one SystemTray + WebviewWindow per project, action dispatch
frontend/
  index.html       Single-file vanilla JS popover (no build step)
assets/icons/      PDF tray icons: idle/busy/mixed/starting
```

### Data flow

1. `config.LoadDir` reads `~/.config/einhasad-bar/*.einhasad-bar.yaml` (registered as symlinks).
2. A 2s ticker calls `ui.Controller.Refresh()` → `state.Build()` → `health.Probe()` + `supervisor.Alive()`.
3. The snapshot is JSON-marshalled and emitted as Wails event `eb:state` to all open popovers.
4. The frontend JS receives `eb:state` and re-renders. Button clicks emit `eb:action` back to Go.
5. `ui.dispatch()` handles the action (start/stop/openurl/taillog/startall/stopall/quit), then calls `Refresh()` immediately so the popover updates without waiting for the next tick.

### Service modes

- **process**: einhasad-bar spawns and supervises the command (pidfile in `~/Library/Caches/einhasad-bar/<project>/`). Stop sends SIGTERM to the process group, escalating to SIGKILL after 8s.
- **watch**: einhasad-bar only monitors. Health = check result only. Optional `start`/`stop` commands add buttons: Start runs detached (`Setsid`), Stop runs to completion.

### Tray icon colour logic (`ui.iconFor`)

- All required services up → busy (green)
- Any required service starting (process alive, check not yet passing) → starting (blue, pulsing)
- Some up, some down → mixed (amber)
- All down → idle (grey)

### Wire protocol

No Wails bindings are used — just two custom events:
- `eb:state` — Go→JS, JSON string of `state.Snapshot`
- `eb:action` — JS→Go, JSON string `{op, project, service}`

### Registration

`einhasad-bar register <path>` validates the YAML and symlinks it as `<project.id>.einhasad-bar.yaml` in `~/.config/einhasad-bar/`, which is the directory the app scans at startup.

### Frontend

`frontend/index.html` is a single vanilla JS file embedded via `//go:embed all:frontend`. No build step. The Wails runtime is served at `/wails/runtime.js`. Each popover window opens at `/?p=<project-id>`.
