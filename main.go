// einhasad-bar is a macOS menu-bar manager for local dev stacks, configured
// declaratively via *.einhasad-bar.yaml files (one per project) instead of the
// bespoke SwiftBar bash scripts it replaces. It shows one menu-bar icon per
// project; clicking an icon opens a reactive webview popover that live-updates
// service health (green/blue/amber/grey) and offers Start/Stop/Open/Tail-log.
package main

import (
	"embed"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/einhasad/einhasad-bar/internal/config"
	"github.com/einhasad/einhasad-bar/internal/health"
	"github.com/einhasad/einhasad-bar/internal/paths"
	"github.com/einhasad/einhasad-bar/internal/supervisor"
	"github.com/einhasad/einhasad-bar/internal/ui"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend
var frontendFS embed.FS

// Tray icon bytes (iconIdle/iconBusy/iconMixed/iconStarting) are embedded per
// OS in icons_darwin.go (PDF) and icons_linux.go (PNG).

// pollInterval is how often health is re-checked and pushed to the popovers.
const pollInterval = 2 * time.Second

// version is overridden at release time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	augmentPATH()

	// Internal flag: forked by "up -d" to run the GUI in the background.
	if len(os.Args) >= 2 && os.Args[1] == "--_app" {
		if err := launchApp(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}

	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		if err := runCommand(os.Args[1], os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}

	fmt.Fprintf(os.Stderr, "usage: einhasad-bar <up [-d] [files...]|down [--id=ID] [files...]|start [-f file] [--foreground] [service...]|stop [-f file] [service...]|restart [-d] [files...]|status [files...]|logs [-f] [--tail=N] <service> [files...]|run <action> [files...]|version>\n")
	os.Exit(1)
}

// augmentPATH prepends common tool locations to PATH so a menu-bar app launched
// at login (with a minimal PATH) can still find the binaries it manages —
// mysqld, redis, node/npm, dotenvx, go. macOS Homebrew and Linux system/Go dirs
// are both listed; os.Stat skips any that don't exist, so the list is shared.
// This is the one piece of environment every SwiftBar plugin had to set by hand.
func augmentPATH() {
	candidates := []string{
		// macOS (Homebrew)
		"/opt/homebrew/bin",
		"/opt/homebrew/sbin",
		"/opt/homebrew/opt/node@22/bin",
		"/usr/local/bin",
		// Linux
		"/usr/local/go/bin",
		"/snap/bin",
		"/usr/sbin",
	}
	cur := os.Getenv("PATH")
	var add []string
	for _, d := range candidates {
		if st, err := os.Stat(d); err == nil && st.IsDir() && !strings.Contains(cur, d) {
			add = append(add, d)
		}
	}
	if len(add) > 0 {
		os.Setenv("PATH", strings.Join(add, ":")+":"+cur)
	}
}

func runCommand(cmd string, args []string) error {
	switch cmd {
	case "up":
		return runUp(args)
	case "down":
		return runDown(args)
	case "start":
		return runStart(args)
	case "stop":
		return runStop(args)
	case "restart":
		return runRestart(args)
	case "status":
		return runStatus(args)
	case "logs":
		return runLogs(args)
	case "run":
		return runAction(args)
	case "version":
		fmt.Printf("einhasad-bar %s\n", version)
		return nil
	default:
		return fmt.Errorf("unknown command %q (want: up | down | start | stop | restart | status | logs | run | version)", cmd)
	}
}

// runUp loads config file(s), checks uniqueness, and either forks the GUI
// process into the background (-d) or runs it in the foreground (blocks).
func runUp(args []string) error {
	fset := flag.NewFlagSet("up", flag.ContinueOnError)
	detach := fset.Bool("d", false, "run in the background (detach)")
	if err := fset.Parse(args); err != nil {
		return err
	}

	filePaths := fset.Args()
	if len(filePaths) == 0 {
		filePaths = []string{"einhasad-bar.yaml"}
	}
	for i, p := range filePaths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return err
		}
		filePaths[i] = abs
	}

	proj, err := config.LoadFiles(filePaths)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if *detach {
		// Try the lock before forking so the parent can report a clean error.
		// The child will re-acquire it in launchApp.
		pidPath, err := paths.AppPidFile(proj.ID)
		if err != nil {
			return err
		}
		lf, err := acquireLock(pidPath, proj.ID)
		if err != nil {
			return err
		}
		lf.Close() // release; child will re-acquire
		return forkExecApp(proj.ID, filePaths)
	}
	return launchApp(filePaths)
}

// runDown sends SIGTERM to the einhasad-bar process managing the given project.
// With no --id it reads the project ID from einhasad-bar.yaml in the CWD.
func runDown(args []string) error {
	fset := flag.NewFlagSet("down", flag.ContinueOnError)
	id := fset.String("id", "", "project ID to stop (GUI only; skips service teardown)")
	if err := fset.Parse(args); err != nil {
		return err
	}

	projectID := *id
	// With no --id we load the config so `down` can also stop the project's
	// services (docker-compose `down`), not just the tray. --id stays a shortcut
	// for killing a tray by id from anywhere, without a config in reach.
	if projectID == "" {
		filePaths := fset.Args()
		if len(filePaths) == 0 {
			filePaths = []string{"einhasad-bar.yaml"}
		}
		abs, err := filepath.Abs(filePaths[0])
		if err != nil {
			return err
		}
		proj, err := config.LoadFiles([]string{abs})
		if err != nil {
			return fmt.Errorf("no --id given and could not read config: %w", err)
		}
		projectID = proj.ID
		targets, _, _ := selectProcessServices(proj, nil, false)
		fmt.Printf("%s (%s) — stopping\n", proj.Name, proj.ID)
		stopProcessServices(supervisor.New(), proj.ID, targets, nil)
	}

	// Stop the tray/monitor GUI if it happens to be running — not an error if it
	// isn't (services can run headless, with no tray).
	pidPath, err := paths.AppPidFile(projectID)
	if err != nil {
		return err
	}
	if pid, ok := readPID(pidPath); ok && syscall.Kill(pid, 0) == nil {
		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
			return fmt.Errorf("kill %d: %w", pid, err)
		}
		fmt.Printf("stopped tray %q (pid %d)\n", projectID, pid)
	}
	return nil
}

// runRestart stops the running project (waiting up to 5s) then starts it again.
func runRestart(args []string) error {
	fset := flag.NewFlagSet("restart", flag.ContinueOnError)
	detach := fset.Bool("d", false, "restart in the background")
	if err := fset.Parse(args); err != nil {
		return err
	}

	filePaths := fset.Args()
	if len(filePaths) == 0 {
		filePaths = []string{"einhasad-bar.yaml"}
	}
	for i, p := range filePaths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return err
		}
		filePaths[i] = abs
	}

	proj, err := config.LoadFiles(filePaths)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	pidPath, err := paths.AppPidFile(proj.ID)
	if err != nil {
		return err
	}
	if pid, ok := readPID(pidPath); ok && syscall.Kill(pid, 0) == nil {
		fmt.Printf("stopping %q (pid %d)...\n", proj.ID, pid)
		syscall.Kill(pid, syscall.SIGTERM)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if syscall.Kill(pid, 0) != nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if syscall.Kill(pid, 0) == nil {
			return fmt.Errorf("project %q did not stop within 5s", proj.ID)
		}
	}

	if *detach {
		return forkExecApp(proj.ID, filePaths)
	}
	return launchApp(filePaths)
}

// runStatus prints the health of every service in the project.
func runStatus(args []string) error {
	fset := flag.NewFlagSet("status", flag.ContinueOnError)
	if err := fset.Parse(args); err != nil {
		return err
	}

	filePaths := fset.Args()
	if len(filePaths) == 0 {
		filePaths = []string{"einhasad-bar.yaml"}
	}
	for i, p := range filePaths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return err
		}
		filePaths[i] = abs
	}

	proj, err := config.LoadFiles(filePaths)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	pidPath, _ := paths.AppPidFile(proj.ID)
	appState := "not running"
	if pid, ok := readPID(pidPath); ok && syscall.Kill(pid, 0) == nil {
		appState = fmt.Sprintf("running (pid %d)", pid)
	}

	var reqUp, reqTotal int
	type row struct {
		label    string
		check    string
		optional bool
		up       bool
		reason   string
	}
	rows := make([]row, len(proj.Services))
	for i, svc := range proj.Services {
		up, reason := serviceHealth(proj.ID, svc)
		rows[i] = row{
			label:    svc.Label,
			check:    healthDesc(svc),
			optional: !svc.IsRequired(),
			up:       up,
			reason:   reason,
		}
		if svc.IsRequired() {
			reqTotal++
			if up {
				reqUp++
			}
		}
	}

	fmt.Printf("%s (%s)  %d/%d required up  [%s]\n\n", proj.Name, proj.ID, reqUp, reqTotal, appState)
	for _, r := range rows {
		marker := "up  "
		if !r.up {
			marker = "down"
		}
		opt := ""
		if r.optional {
			opt = " (optional)"
		}
		if r.reason != "" {
			fmt.Printf("  [%s]  %-20s  %-28s%s  — %s\n", marker, r.label, r.check, opt, r.reason)
		} else {
			fmt.Printf("  [%s]  %-20s  %-28s%s\n", marker, r.label, r.check, opt)
		}
	}
	return nil
}

// serviceHealth probes a service and returns (up, reason).
// For process-mode services with no check it falls back to the supervisor pidfile.
func serviceHealth(projectID string, svc config.Service) (bool, string) {
	if svc.Check != nil {
		return health.ProbeReason(svc.Check)
	}
	if svc.Mode == config.ModeProcess {
		pidPath, err := paths.PidFile(projectID, svc.ID)
		if err != nil {
			return false, err.Error()
		}
		data, err := os.ReadFile(pidPath)
		if err != nil {
			return false, "not started"
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil || pid <= 0 || syscall.Kill(pid, 0) != nil {
			return false, "process not running"
		}
		return true, ""
	}
	return false, "no check"
}

func healthDesc(svc config.Service) string {
	if svc.Check != nil {
		return health.String(svc.Check)
	}
	if svc.Mode == config.ModeProcess {
		return "process"
	}
	return ""
}

// launchApp loads the config, acquires an exclusive flock on the PID file
// (the OS releases it automatically on process exit), and runs the Wails GUI.
// Called from runUp (foreground) and --_app (daemon child).
func launchApp(filePaths []string) error {
	proj, err := config.LoadFiles(filePaths)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	pidPath, err := paths.AppPidFile(proj.ID)
	if err != nil {
		return err
	}
	// Acquire exclusive lock. The OS releases it when this process exits for
	// any reason — no manual cleanup or signal handler needed.
	lf, err := acquireLock(pidPath, proj.ID)
	if err != nil {
		return err
	}
	lf.Truncate(0)
	fmt.Fprintf(lf, "%d", os.Getpid()) // write PID for 'down' to read
	// lf is intentionally not closed — keeping it open holds the flock.

	frontend, err := fs.Sub(frontendFS, "frontend")
	if err != nil {
		return fmt.Errorf("frontend assets: %w", err)
	}

	app := application.New(application.Options{
		Name:   "einhasad-bar",
		Assets: application.AssetOptions{Handler: application.BundledAssetFileServer(frontend)},
		Mac: application.MacOptions{
			ActivationPolicy: application.ActivationPolicyAccessory,
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
		Windows: application.WindowsOptions{DisableQuitOnLastWindowClosed: true},
	})

	sup := supervisor.New()
	ctrl := ui.New(app, sup, ui.Icons{
		Idle:     iconIdle,
		Busy:     iconBusy,
		Mixed:    iconMixed,
		Starting: iconStarting,
	}, []config.Project{proj})

	go func() {
		// docker-compose-style `up`: bring the required process services up as the
		// tray appears (idempotent — Start no-ops on a service that's already
		// alive, so re-running `up` is safe). Watch services stay external.
		targets, _, _ := selectProcessServices(proj, nil, true)
		startProcessServices(sup, proj.ID, targets, nil)
		ctrl.Refresh()
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for range ticker.C {
			ctrl.Refresh()
		}
	}()

	return app.Run()
}

// forkExecApp re-execs itself with --_app and the given file paths, detached
// from the terminal (new session). The parent returns immediately.
func forkExecApp(projectID string, filePaths []string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find executable: %w", err)
	}
	args := append([]string{"--_app"}, filePaths...)
	cmd := exec.Command(self, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	devNull, err := os.OpenFile("/dev/null", os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer devNull.Close()
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start background: %w", err)
	}
	fmt.Printf("started project %q in background (pid %d)\n", projectID, cmd.Process.Pid)
	return nil
}

// acquireLock opens pidPath and tries a non-blocking exclusive flock.
// Returns the open file (caller must keep it open to hold the lock) or an
// error if the lock is already held by another process.
func acquireLock(pidPath, projectID string) (*os.File, error) {
	f, err := os.OpenFile(pidPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		pid, ok := readPID(pidPath)
		f.Close()
		if ok {
			return nil, fmt.Errorf("project %q is already running (pid %d)\nuse: einhasad-bar down --id=%s", projectID, pid, projectID)
		}
		return nil, fmt.Errorf("project %q is already running", projectID)
	}
	return f, nil
}

func readPID(path string) (int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// runAction runs a project-level action by its id, to completion, wiring the
// command's stdio to the terminal. Usage: einhasad-bar run <action> [files...]
func runAction(args []string) error {
	fset := flag.NewFlagSet("run", flag.ContinueOnError)
	if err := fset.Parse(args); err != nil {
		return err
	}

	rest := fset.Args()
	if len(rest) == 0 {
		return fmt.Errorf("usage: einhasad-bar run <action> [files...]")
	}
	actionID := rest[0]

	filePaths := rest[1:]
	if len(filePaths) == 0 {
		filePaths = []string{"einhasad-bar.yaml"}
	}
	for i, p := range filePaths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return err
		}
		filePaths[i] = abs
	}

	proj, err := config.LoadFiles(filePaths)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	var action *config.Action
	var ids []string
	for i := range proj.Actions {
		if proj.Actions[i].ID != "" {
			ids = append(ids, proj.Actions[i].ID)
		}
		if proj.Actions[i].ID == actionID {
			action = &proj.Actions[i]
		}
	}
	if action == nil {
		return fmt.Errorf("unknown action %q (available: %s)", actionID, strings.Join(ids, ", "))
	}

	cmd := action.Cmd()
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Passthrough: the action's own output and exit code are what matter, so
	// surface the child's status directly instead of wrapping it in a logger
	// line. This lets `einhasad-bar run <action>` chain like a task runner.
	err = cmd.Run()
	if exitErr, ok := err.(*exec.ExitError); ok {
		os.Exit(exitErr.ExitCode())
	}
	return err
}

// lifecycleFlags parses the shared "-f <file>" flag used by start/stop and
// returns the absolute config path plus the remaining positional service names.
func lifecycleFlags(name string, args []string) (string, []string, error) {
	fset := flag.NewFlagSet(name, flag.ContinueOnError)
	file := fset.String("f", "einhasad-bar.yaml", "config file (auto-merges siblings)")
	if err := fset.Parse(args); err != nil {
		return "", nil, err
	}
	abs, err := filepath.Abs(*file)
	if err != nil {
		return "", nil, err
	}
	return abs, fset.Args(), nil
}

// selectProcessServices picks the process-mode services to act on, in declaration
// order and deduplicated by Group. When names is non-empty it selects exactly
// those ids (an unknown id is an error; a watch id is returned in skipped so the
// caller can report it). When names is empty it selects every process service,
// limited to required ones when requiredOnly is set. Watch services (externally
// managed daemons) are never started/stopped by the lifecycle commands.
func selectProcessServices(proj config.Project, names []string, requiredOnly bool) (targets, skipped []config.Service, err error) {
	byID := make(map[string]config.Service, len(proj.Services))
	ids := make([]string, 0, len(proj.Services))
	for _, svc := range proj.Services {
		byID[svc.ID] = svc
		ids = append(ids, svc.ID)
	}

	var chosen []config.Service
	if len(names) > 0 {
		for _, n := range names {
			svc, ok := byID[n]
			if !ok {
				return nil, nil, fmt.Errorf("unknown service %q (available: %s)", n, strings.Join(ids, ", "))
			}
			if svc.Mode != config.ModeProcess {
				skipped = append(skipped, svc)
				continue
			}
			chosen = append(chosen, svc)
		}
	} else {
		for _, svc := range proj.Services {
			if svc.Mode != config.ModeProcess {
				continue
			}
			if requiredOnly && !svc.IsRequired() {
				continue
			}
			chosen = append(chosen, svc)
		}
	}

	// Deduplicate by Group so services sharing a lifecycle are acted on once.
	seen := map[string]bool{}
	for _, svc := range chosen {
		if svc.Group != "" {
			if seen[svc.Group] {
				continue
			}
			seen[svc.Group] = true
		}
		targets = append(targets, svc)
	}
	return targets, skipped, nil
}

// startProcessServices starts each target via the supervisor (idempotent) and
// prints a per-service line. Returns the number of failures.
func startProcessServices(sup *supervisor.Supervisor, projectID string, targets, skipped []config.Service) int {
	fails := 0
	for _, svc := range targets {
		switch {
		case sup.Alive(projectID, svc.ID):
			fmt.Printf("  [start] %-16s already running\n", svc.Label)
		default:
			if err := sup.Start(projectID, svc); err != nil {
				fmt.Printf("  [start] %-16s ERROR: %v\n", svc.Label, err)
				fails++
				continue
			}
			fmt.Printf("  [start] %-16s started\n", svc.Label)
		}
	}
	for _, svc := range skipped {
		fmt.Printf("  [skip ] %-16s watch — externally managed\n", svc.Label)
	}
	return fails
}

// stopProcessServices stops each target via the supervisor, in reverse order.
func stopProcessServices(sup *supervisor.Supervisor, projectID string, targets, skipped []config.Service) {
	for i := len(targets) - 1; i >= 0; i-- {
		svc := targets[i]
		if !sup.Alive(projectID, svc.ID) {
			fmt.Printf("  [stop ] %-16s not running\n", svc.Label)
			continue
		}
		_ = sup.Stop(projectID, svc)
		fmt.Printf("  [stop ] %-16s stopped\n", svc.Label)
	}
	for _, svc := range skipped {
		fmt.Printf("  [skip ] %-16s watch — externally managed\n", svc.Label)
	}
}

// runStart starts process-mode services headlessly (no GUI, no app lock), so it
// can be called by an action or an agent even while the tray is running. With no
// service names it starts the required process services (like the tray's "Start
// server"). Usage: einhasad-bar start [-f file] [service...]
func runStart(args []string) error {
	fset := flag.NewFlagSet("start", flag.ContinueOnError)
	file := fset.String("f", "einhasad-bar.yaml", "config file (auto-merges siblings)")
	fg := fset.Bool("foreground", false, "run one named service attached to this terminal (Ctrl-C stops it)")
	if err := fset.Parse(args); err != nil {
		return err
	}
	path, err := filepath.Abs(*file)
	if err != nil {
		return err
	}
	names := fset.Args()
	proj, err := config.LoadFiles([]string{path})
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if *fg {
		return runForeground(proj, names)
	}

	targets, skipped, err := selectProcessServices(proj, names, len(names) == 0)
	if err != nil {
		return err
	}
	fmt.Printf("%s (%s) — starting\n", proj.Name, proj.ID)
	if fails := startProcessServices(supervisor.New(), proj.ID, targets, skipped); fails > 0 {
		return fmt.Errorf("%d service(s) failed to start", fails)
	}
	return nil
}

// runForeground runs a single named process service attached to the terminal
// (no detach, no pidfile) so its output streams live and Ctrl-C stops it — the
// supervised analog of running its command by hand.
func runForeground(proj config.Project, names []string) error {
	if len(names) != 1 {
		return fmt.Errorf("--foreground takes exactly one service (e.g. einhasad-bar start --foreground grpc_server)")
	}
	var svc *config.Service
	var ids []string
	for i := range proj.Services {
		ids = append(ids, proj.Services[i].ID)
		if proj.Services[i].ID == names[0] {
			svc = &proj.Services[i]
		}
	}
	if svc == nil {
		return fmt.Errorf("unknown service %q (available: %s)", names[0], strings.Join(ids, ", "))
	}
	if svc.Mode != config.ModeProcess {
		return fmt.Errorf("service %q is %s, not a process — nothing to run in foreground", svc.ID, svc.Mode)
	}
	sup := supervisor.New()
	if sup.Alive(proj.ID, svc.ID) {
		return fmt.Errorf("service %q is already running supervised — stop it first: einhasad-bar stop %s", svc.ID, svc.ID)
	}
	fmt.Printf("%s (%s) — %s in foreground (Ctrl-C to stop)\n", proj.Name, proj.ID, svc.Label)
	err := sup.RunForeground(*svc)
	if _, ok := err.(*exec.ExitError); ok {
		return nil // exited / interrupted — expected for a foreground run
	}
	return err
}

// runStop stops process-mode services headlessly. With no names it stops all
// process services (incl. optional). Usage: einhasad-bar stop [-f file] [service...]
func runStop(args []string) error {
	path, names, err := lifecycleFlags("stop", args)
	if err != nil {
		return err
	}
	proj, err := config.LoadFiles([]string{path})
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	targets, skipped, err := selectProcessServices(proj, names, false)
	if err != nil {
		return err
	}
	fmt.Printf("%s (%s) — stopping\n", proj.Name, proj.ID)
	stopProcessServices(supervisor.New(), proj.ID, targets, skipped)
	return nil
}

// runLogs streams the log file for a service, similar to kubectl logs.
// Usage: einhasad-bar logs [-f] [--tail=N] <service> [files...]
func runLogs(args []string) error {
	fset := flag.NewFlagSet("logs", flag.ContinueOnError)
	follow := fset.Bool("f", false, "stream log output (follow)")
	tailN := fset.Int("tail", 20, "lines from end to show (-1 = all)")
	if err := fset.Parse(args); err != nil {
		return err
	}

	rest := fset.Args()
	if len(rest) == 0 {
		return fmt.Errorf("usage: einhasad-bar logs [-f] [--tail=N] <service> [files...]")
	}
	serviceID := rest[0]

	filePaths := rest[1:]
	if len(filePaths) == 0 {
		filePaths = []string{"einhasad-bar.yaml"}
	}
	for i, p := range filePaths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return err
		}
		filePaths[i] = abs
	}

	proj, err := config.LoadFiles(filePaths)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	found := false
	for _, svc := range proj.Services {
		if svc.ID == serviceID {
			found = true
			break
		}
	}
	if !found {
		var ids []string
		for _, svc := range proj.Services {
			ids = append(ids, svc.ID)
		}
		return fmt.Errorf("unknown service %q (available: %s)", serviceID, strings.Join(ids, ", "))
	}

	logPath, err := paths.LogFile(proj.ID, serviceID)
	if err != nil {
		return err
	}

	f, err := os.Open(logPath)
	if os.IsNotExist(err) {
		return fmt.Errorf("no log for %q — has it been started?", serviceID)
	}
	if err != nil {
		return err
	}
	defer f.Close()

	if err := tailLines(f, *tailN); err != nil {
		return err
	}
	if !*follow {
		return nil
	}

	buf := make([]byte, 32*1024)
	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			os.Stdout.Write(buf[:n])
		}
		if readErr == io.EOF {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if readErr != nil {
			return readErr
		}
	}
}

// tailLines prints the last n lines of f and leaves the read position at EOF.
// n < 0 prints the whole file; n == 0 prints nothing.
func tailLines(f *os.File, n int) error {
	if n == 0 {
		return nil
	}
	if n < 0 {
		_, err := io.Copy(os.Stdout, f)
		return err
	}

	info, err := f.Stat()
	if err != nil {
		return err
	}
	size := info.Size()
	if size == 0 {
		return nil
	}

	const chunkSize = 4096
	buf := make([]byte, chunkSize)
	pos := size
	newlines := 0
	startPos := int64(0)

outer:
	for pos > 0 {
		read := int64(chunkSize)
		if pos < read {
			read = pos
		}
		pos -= read
		if _, err := f.Seek(pos, io.SeekStart); err != nil {
			return err
		}
		nr, err := f.Read(buf[:read])
		if err != nil && err != io.EOF {
			return err
		}
		for i := nr - 1; i >= 0; i-- {
			if buf[i] == '\n' {
				newlines++
				if newlines > n {
					startPos = pos + int64(i) + 1
					break outer
				}
			}
		}
	}

	if _, err := f.Seek(startPos, io.SeekStart); err != nil {
		return err
	}
	_, err = io.Copy(os.Stdout, f)
	return err
}
