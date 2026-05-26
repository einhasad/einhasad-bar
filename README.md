# einhasad-bar

A macOS **menu-bar manager for local dev stacks**, configured declaratively the
way `docker-compose.yaml` configures containers. One `einhasad-bar.yaml` per
project becomes one menu-bar icon with live health polling and lifecycle control
([Wails v3](https://v3.wails.io); menu-bar only — no dock icon, no window).

It replaces per-project shell glue (SwiftBar plugins, pidfile wranglers) with a
single binary: declare your services once, then get Start/Stop, health colours,
and log tailing for free.

## Install

```bash
brew install einhasad/tap/einhasad-bar
```

Or build from source (Go 1.25+, Xcode command-line tools, CGO enabled):

```bash
go build -o einhasad-bar . && ./einhasad-bar up
```

### Linux (experimental)

einhasad-bar also runs on Linux (Wails v3 GTK/WebKit).

**Prebuilt — Debian / Ubuntu, x86_64.** Each release ships a `linux_amd64`
tarball (built on Ubuntu 22.04, so it also runs on 24.04). Install the runtime
libraries, then download from the [releases page](https://github.com/einhasad/einhasad-bar/releases):

```bash
sudo apt install -y libgtk-3-0 libwebkit2gtk-4.1-0 xdg-utils
tar -xzf einhasad-bar_*_linux_amd64.tar.gz
sudo install einhasad-bar /usr/local/bin/
```

The binary is dynamically linked against GTK/WebKit and glibc, so it is **not**
portable to other distro families or architectures — those build from source.

#### Build from source

**1. Install the build dependencies** (Go 1.25+, a C toolchain, GTK 3, WebKitGTK):

```bash
# Debian / Ubuntu (22.04, 24.04), and other webkit2gtk-4.1 distros
sudo apt install -y build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev

# Fedora 40+ / Arch / openSUSE — ship WebKitGTK 6.0 instead (package names vary):
#   Fedora: gtk3-devel webkit2gtk6.0-devel
#   Arch:   gtk3 webkitgtk-6.0
```

**2. Build with the WebKit flavour your distro ships:**

```bash
# webkit2gtk-4.1 (Ubuntu/Debian, Fedora ≤39, RHEL 9) — legacy path, needs the tag:
go build -tags gtk3 -o einhasad-bar .

# webkitgtk-6.0 (Fedora 40+, Arch, …) — the Wails v3 default, no tag:
go build -o einhasad-bar .
```

(The `gtk3` legacy path is supported through Wails v3.0.x.)

**Runtime requirements:**

- A **StatusNotifier-capable system tray**: XFCE, KDE, Cinnamon, and MATE work
  out of the box; **stock GNOME shows nothing without the AppIndicator extension**.
- **xdg-utils** (`xdg-open`) for the Open / Log buttons.
- State lives under XDG dirs: `~/.cache/einhasad-bar/` (pidfiles + lock) and
  `~/.local/state/einhasad-bar/logs/` (service logs).

## Quick start

```bash
# 1. Drop an einhasad-bar.yaml in your project root (see Configuration below).
cd ~/www/myproject

# 2. Launch the menu-bar app for it, detached into the background.
einhasad-bar up -d

# 3. A tray icon appears. Left-click it for the popover; right-click for actions.
```

`up` reads `einhasad-bar.yaml` from the current directory by default. To run it
automatically at login, add a **System Settings → General → Login Items** entry
that runs `einhasad-bar up -d /path/to/myproject/einhasad-bar.yaml`.

## CLI

```
einhasad-bar <command> [flags] [files...]
```

| Command | Effect |
|---|---|
| `up [-d] [files...]` | Run the menu-bar app for the given config(s). `-d` detaches into the background; otherwise it runs in the foreground. |
| `down [--id=ID]` | Stop the running app for a project. With no `--id`, reads the id from `einhasad-bar.yaml` in the current directory. |
| `restart [-d] [files...]` | Stop the running project (waits up to 5s) then start it again. |
| `status [files...]` | Print a one-shot health table for every service, without launching the GUI. |
| `version` | Print the version. |

Files default to `einhasad-bar.yaml` in the current directory. Passing **several
files merges them into one project** — they must all agree on `project.id`, and
their services and actions are combined.

A project may only run once at a time: `up` takes an exclusive `flock` on the
project's pidfile, so a second `up` reports the already-running PID instead of
starting a duplicate.

## Configuration

```yaml
project:
  name: "My Stack"   # menu header (defaults to id)
  id: "demo"         # tray label + state/log dir name (required, lowercase)

services:
  # process — einhasad-bar spawns and supervises it (Start/Stop buttons, pidfile,
  # live CPU/RSS). A `command` with no `mode` is inferred as process.
  - id: api
    label: "API"
    command: make
    args: ["run"]
    working_dir: "$PROJECT_DIR/server"
    env_file: ".env"               # optional plain .env (relative to working_dir)
    env: { LOG_LEVEL: "debug" }    # optional static overrides (win over env_file)
    url: "http://localhost:8080/"  # adds an "Open" button
    check: { type: tcp, port: 8080 } # readiness probe; omit → "alive = up"

  # watch — an externally-managed daemon. einhasad-bar only monitors it; health
  # comes entirely from `check`. No Start/Stop button.
  - id: database
    label: "Database"
    mode: watch
    required: false                  # optional: never flips the icon colour
    check: { type: tcp, port: 5432 }

# actions — project-level commands shown in the tray right-click menu and the
# popover footer. Use these for "start everything" style scripts.
actions:
  - label: "Start server"
    command: "$PROJECT_DIR/dev/server"
    args: ["up"]
  - label: "Stop server"
    command: "$PROJECT_DIR/dev/server"
    args: ["down"]
```

### Schema reference

| Field | Notes |
|---|---|
| `project.id` | Required, lowercase. Also the tray label (`DEMO 2/2`) and the state/log dir name. |
| `project.name` | Menu header. Defaults to the id. |
| `services[].mode` | `process` (managed) or `watch` (monitored). A `command` with no mode ⇒ `process`. |
| `services[].required` | Defaults `true`. Optional services never flip the icon colour. |
| `services[].check` | `{type: tcp, port}` · `{type: http, url}` (passes on 2xx/3xx) · `{type: pidfile, pidfile}`. |
| `services[].command` / `args` / `working_dir` | Process spawn (process mode only). |
| `services[].env_file` / `env` | Plain `.env` (godotenv) then static `KEY: value` overrides. |
| `services[].url` | Adds an "Open" button to the popover row. |
| `services[].group` | Services sharing a group are stopped once (not once per member) when the app quits. |
| `actions[]` | Project-level commands: `{label, command, args, working_dir, env}`. Run to completion on click. |
| `include` | List of other config files to merge in (each loads with its own `$PROJECT_DIR`). |
| `variables` | `KEY: value` map usable as `$KEY` anywhere in the file. |

**Variable expansion.** Any string is expanded with `$PROJECT_DIR` (the
directory of the config file), declared `variables`, environment variables, and
a leading `~`.

**Sibling auto-merge.** Loading `einhasad-bar.yaml` also picks up sibling
`einhasad-bar.*.yaml` files in the same directory, merged by service id, with
`*.local.yaml` always loaded last so it can override anything (handy for
machine-specific paths kept out of git).

> For dotenvx-encrypted envs, wrap at the command level rather than using
> `env_file`: `command: dotenvx`, `args: [run, -f, .env, --, make, run]`.

## Behaviour

- **process** services run in their own process group, log to
  `~/Library/Logs/einhasad-bar/<project>/<service>.log`, and are tracked by a
  pidfile (state survives an app restart). Stop sends `SIGTERM` to the whole
  group, escalating to `SIGKILL` after 8s.
- **watch** services are monitored via their `check` only — no pidfile, no
  Start/Stop button. Health is the single source of truth.
- Health is polled every 2s and pushed to the popovers and tray icons.

### Status icon

Colour reflects **required** services only:

- 🟢 green — all required up
- 🔵 blue — at least one starting (process alive, check not passing yet; pulsing)
- 🟠 amber — some up, some down
- ⚫ grey — all down

While a project-level action is running, the icon goes blue and the label shows
`ID…`.

## UI

**Left-click** a tray icon for a reactive webview popover (hides on focus loss);
it live-updates over events so you watch a service go blue→green in real time.
Each row has a status dot, label, an info line (CPU/RSS/pid for running
processes, or `starting… waiting for tcp:8080`), and buttons: Start/Stop (process
services), Open (if a `url` is set), Log. The footer holds your `actions`.

**Right-click** the icon for a native menu: your `actions`, then Refresh and
Quit.

> The popover is a webview, not a native menu: a native macOS menu freezes while
> open (the OS blocks the main dispatch queue during menu tracking), which would
> stop the live updates.

## File locations

| Path | Contents |
|---|---|
| `~/Library/Caches/einhasad-bar/<id>/<service>.pid` | Per-service supervisor pidfiles. |
| `~/Library/Caches/einhasad-bar/<id>/einhasad-bar.pid` | App lock + PID, read by `down`/`restart`. |
| `~/Library/Logs/einhasad-bar/<id>/<service>.log` | Per-service stdout/stderr. |

## Releasing

Tag `vX.Y.Z` and push — [`.github/workflows/release.yml`](.github/workflows/release.yml)
runs GoReleaser, which builds the darwin/arm64 binary, cuts a GitHub release, and
updates the [Homebrew tap](https://github.com/einhasad/homebrew-tap). Requires a
`HOMEBREW_TAP_TOKEN` secret (a PAT with `Contents: write` on the tap repo).

## Scope

MVP. Not yet: a signed/notarized `.app` bundle (the formula installs the bare
binary, so Gatekeeper may warn on first launch), `launchd` management, install
hooks, and amd64 builds. The schema ignores unknown fields so these slot in later.

## License

See [LICENSE](LICENSE).
