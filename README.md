# einhasad-bar

A macOS **menu-bar manager for local dev stacks**, configured declaratively the
way `docker-compose.yaml` configures containers. One `*.einhasad-bar.yaml` per
project becomes one menu-bar icon with live health polling and lifecycle control
([Wails v3](https://v3.wails.io); menu-bar only — no dock icon, no window).

It replaces per-project shell glue (SwiftBar plugins, pidfile wranglers) with a
single app: declare your services once, get Start/Stop, health colours, and log
tailing for free.

## Install

```bash
brew install einhasad/tap/einhasad-bar
```

Or build from source (Go 1.25+, Xcode command-line tools):

```bash
go build -o einhasad-bar . && ./einhasad-bar
```

## Quick start

```bash
# 1. Write an einhasad-bar.yaml in your project (see example.einhasad-bar.yaml).
# 2. Register it — symlinks it into ~/.config/einhasad-bar/.
einhasad-bar register ~/www/myproject/einhasad-bar.yaml
einhasad-bar list

# 3. Launch the menu-bar app (one icon per registered project).
einhasad-bar &
```

To start it automatically, add einhasad-bar under
**System Settings → General → Login Items**.

| Command | Effect |
|---|---|
| `einhasad-bar register <file>` | Validate a config and add its icon. |
| `einhasad-bar unregister <id>` | Remove a project by its `project.id`. |
| `einhasad-bar list` | Show registered projects. |
| `einhasad-bar` | Run the menu-bar app over the registered configs. |
| `einhasad-bar version` | Print the version. |

## Configuration

```yaml
project:
  name: "AlmaWord"   # menu header (defaults to id)
  id: "aw"           # tray label + state/log dir name

services:
  # process: einhasad-bar spawns and supervises it.
  - id: api
    command: make
    args: ["run"]
    working_dir: "/path/to/server"
    url: "http://localhost:4444/"
    check: { type: tcp, port: 4444 }

  # watch: an external daemon. Health is the check; start/stop are optional and
  # give it Start/Stop buttons without einhasad-bar owning the process.
  - id: database
    mode: watch
    check: { type: tcp, port: 3306 }
    start: { command: /opt/homebrew/bin/mysqld, args: ["--defaults-file=/path/my.cnf"] }
    stop:  { command: mysqladmin, args: ["-uroot", "shutdown"] }
```

See [`example.einhasad-bar.yaml`](example.einhasad-bar.yaml) for the full schema.

### Schema reference

| Field | Notes |
|---|---|
| `project.id` | Required, lowercase. Also the tray label (`AW 3/3`). |
| `service.mode` | `process` (managed) or `watch` (monitored). A `command` with no mode ⇒ `process`. |
| `service.required` | Defaults `true`. Optional services never flip the icon colour. |
| `service.check` | `{type: tcp, port}` · `{type: http, url}` (2xx/3xx) · `{type: pidfile, pidfile}`. |
| `command`/`args`/`working_dir` | Process spawn (process mode). |
| `start`/`stop` | Lifecycle commands for **watch** services — each `{command, args, working_dir, env_file, env}`. Start runs detached (the daemon outlives the app); Stop runs to completion. |
| `group` | Services sharing a group fire Start/Stop **once** on "Start server" (e.g. a single `docker compose up` shared by several entries). |
| `env_file` | Plain `.env` (godotenv). For dotenvx-encrypted envs, wrap at the command level: `command: dotenvx, args: [run, -f, .env, --, make, run]`. |
| `env` | Static `KEY: value` overrides, applied after `env_file`. |
| `url` | Adds an "Open" button. |

## Behaviour

- **process** services run in their own process group, log to
  `~/Library/Logs/einhasad-bar/<project>/<service>.log`, and are tracked by a
  pidfile in `~/Library/Caches/einhasad-bar/<project>/` (state survives an app
  restart). Stop sends `SIGTERM` to the whole group, escalating to `SIGKILL`
  after 8s.
- **watch** services are monitored via their `check`. With `start`/`stop` they
  gain buttons: Start runs the command in a new session (so the daemon outlives
  einhasad-bar), Stop runs it to completion. Health stays the single source of
  truth — no pidfile.
- Health is polled every 2s and pushed to the popovers and tray icons.

### Status icon

Colour reflects **required** services only:

- 🟢 green — all required up · 🔵 blue — at least one starting (process alive,
  check not passing yet) · 🟠 amber — some up, some down · ⚫ grey — all down.

## UI

Left-click a tray icon for a reactive webview popover (hides on focus loss); it
live-updates over events so you watch a service go blue→green in real time. Each
row has a status dot, label, info line (CPU/RSS/pid for running processes, or
`starting… waiting for tcp:4444`), and buttons: Start/Stop, Open, Log. A footer
has Start server / Stop server / Quit. Right-click gives a native fallback menu.

> The popover is a webview, not a native menu: a native macOS menu freezes while
> open (the OS blocks the main dispatch queue during menu tracking), which would
> stop the live updates.

## Releasing

Tag `vX.Y.Z` and push — [`.github/workflows/release.yml`](.github/workflows/release.yml)
runs GoReleaser, which builds the darwin/arm64 binary, cuts a GitHub release, and
updates the [Homebrew tap](https://github.com/einhasad/homebrew-tap). Requires a
`HOMEBREW_TAP_TOKEN` secret (a PAT with `Contents: write` on the tap repo).

## Scope

MVP. Not yet: a signed/notarized `.app` bundle (the formula installs the bare
binary, so Gatekeeper may warn on first launch), `launchd` management, install
hooks, and amd64 builds. The schema ignores unknown fields so these slot in later.
