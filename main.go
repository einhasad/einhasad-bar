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

//go:embed assets/icons/idle.pdf
var iconIdle []byte

//go:embed assets/icons/busy.pdf
var iconBusy []byte

//go:embed assets/icons/mixed.pdf
var iconMixed []byte

//go:embed assets/icons/starting.pdf
var iconStarting []byte

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

	fmt.Fprintf(os.Stderr, "usage: einhasad-bar <up [-d] [files...]|down [--id=ID]|restart [-d] [files...]|status [files...]|version>\n")
	os.Exit(1)
}

// augmentPATH prepends common Homebrew locations to PATH so a menu-bar app
// launched at login (with the minimal /usr/bin:/bin PATH) can still find
// brew-installed tools — mysqld, redis, node/npm, dotenvx, go. This is the
// one piece of environment every SwiftBar plugin had to set by hand.
func augmentPATH() {
	candidates := []string{
		"/opt/homebrew/bin",
		"/opt/homebrew/sbin",
		"/opt/homebrew/opt/node@22/bin",
		"/usr/local/bin",
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
	case "restart":
		return runRestart(args)
	case "status":
		return runStatus(args)
	case "version":
		fmt.Printf("einhasad-bar %s\n", version)
		return nil
	default:
		return fmt.Errorf("unknown command %q (want: up | down | restart | status | version)", cmd)
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
	id := fset.String("id", "", "project ID to stop")
	if err := fset.Parse(args); err != nil {
		return err
	}

	projectID := *id
	if projectID == "" {
		abs, err := filepath.Abs("einhasad-bar.yaml")
		if err != nil {
			return err
		}
		proj, err := config.LoadFiles([]string{abs})
		if err != nil {
			return fmt.Errorf("no --id given and could not read einhasad-bar.yaml in current directory: %w", err)
		}
		projectID = proj.ID
	}

	pidPath, err := paths.AppPidFile(projectID)
	if err != nil {
		return err
	}
	pid, ok := readPID(pidPath)
	if !ok || syscall.Kill(pid, 0) != nil {
		return fmt.Errorf("project %q is not running", projectID)
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("kill %d: %w", pid, err)
	}
	fmt.Printf("stopped project %q (pid %d)\n", projectID, pid)
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
			ActivationPolicy:                                application.ActivationPolicyAccessory,
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
