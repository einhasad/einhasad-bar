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
	"path/filepath"
	"strings"
	"time"

	"github.com/einhasad/einhasad-bar/internal/config"
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
// Kept short so the blue "starting" state flips to green/grey promptly.
const pollInterval = 2 * time.Second

// version is overridden at release time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	augmentPATH()

	// CLI subcommands (register/unregister/list) come before the menu-bar app.
	// They manage the symlink farm in ~/.config/einhasad-bar/ that the app reads;
	// a leading "-" means flags-only, i.e. just launch the app.
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		if err := runCommand(os.Args[1], os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}

	defaultDir, err := paths.ConfigDir()
	if err != nil {
		log.Fatalf("config dir: %v", err)
	}
	configDir := flag.String("config-dir", defaultDir, "directory of registered *.einhasad-bar.yaml files")
	flag.Parse()

	dir, err := filepath.Abs(*configDir)
	if err != nil {
		log.Fatalf("resolve config dir: %v", err)
	}

	projects, err := config.LoadDir(dir)
	if err != nil {
		log.Fatalf("load configs: %v", err)
	}
	if len(projects) == 0 {
		log.Fatalf("no %s files found in %s", config.Glob, dir)
	}

	// Serve the embedded popover frontend (rooted so index.html is at "/").
	frontend, err := fs.Sub(frontendFS, "frontend")
	if err != nil {
		log.Fatalf("frontend assets: %v", err)
	}

	app := application.New(application.Options{
		Name:   "einhasad-bar",
		Assets: application.AssetOptions{Handler: application.BundledAssetFileServer(frontend)},
		Mac: application.MacOptions{
			// Menu-bar only: no dock icon; don't quit when popovers hide.
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
	}, projects)

	// Poll loop: push state to popovers + repaint tray icons.
	go func() {
		ctrl.Refresh()
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for range ticker.C {
			ctrl.Refresh()
		}
	}()

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// augmentPATH prepends common Homebrew locations to PATH so a menu-bar app
// launched at login (with the minimal /usr/bin:/bin PATH) can still find
// brew-installed tools — mysqld, garage, node/npm (keg-only), dotenvx, go. This
// is the one piece of environment every SwiftBar plugin had to set by hand.
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

// runCommand handles the non-GUI subcommands that manage which project configs
// the menu-bar app shows. Each registration is a symlink in ~/.config/einhasad-bar/
// named "<project.id>.einhasad-bar.yaml" so it matches the app's discovery glob.
func runCommand(cmd string, args []string) error {
	switch cmd {
	case "register":
		if len(args) != 1 {
			return fmt.Errorf("usage: einhasad-bar register <path-to-einhasad-bar.yaml>")
		}
		return registerConfig(args[0])
	case "unregister":
		if len(args) != 1 {
			return fmt.Errorf("usage: einhasad-bar unregister <project-id>")
		}
		return unregisterConfig(args[0])
	case "list":
		return listConfigs()
	case "version":
		fmt.Printf("einhasad-bar %s\n", version)
		return nil
	default:
		return fmt.Errorf("unknown command %q (want: register | unregister | list | version)", cmd)
	}
}

// registerConfig validates the YAML, then symlinks it into the config dir keyed
// by its project id (replacing any prior registration for that id).
func registerConfig(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	proj, err := config.LoadFile(abs) // validates and yields project.id
	if err != nil {
		return fmt.Errorf("invalid config %s: %w", abs, err)
	}
	dir, err := paths.ConfigDir()
	if err != nil {
		return err
	}
	link := filepath.Join(dir, proj.ID+".einhasad-bar.yaml")
	_ = os.Remove(link) // replace an existing registration for this id
	if err := os.Symlink(abs, link); err != nil {
		return err
	}
	fmt.Printf("registered %q (%d services) → %s\n", proj.ID, len(proj.Services), abs)
	return nil
}

func unregisterConfig(id string) error {
	dir, err := paths.ConfigDir()
	if err != nil {
		return err
	}
	link := filepath.Join(dir, id+".einhasad-bar.yaml")
	if err := os.Remove(link); err != nil {
		return fmt.Errorf("not registered: %s", id)
	}
	fmt.Printf("unregistered %q\n", id)
	return nil
}

func listConfigs() error {
	dir, err := paths.ConfigDir()
	if err != nil {
		return err
	}
	projects, err := config.LoadDir(dir)
	if err != nil {
		return err
	}
	if len(projects) == 0 {
		fmt.Printf("no projects registered in %s\n", dir)
		return nil
	}
	for _, p := range projects {
		target, _ := os.Readlink(filepath.Join(dir, p.ID+".einhasad-bar.yaml"))
		fmt.Printf("  %-10s %d services  → %s\n", p.ID, len(p.Services), target)
	}
	return nil
}
