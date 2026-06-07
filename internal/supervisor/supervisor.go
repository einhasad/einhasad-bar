// Package supervisor owns the lifecycle of process-mode services: it spawns
// each command in its own process group (so the whole tree — make → go run →
// compiled binary — can be signalled at once), redirects output to a log file,
// and records the PID in a pidfile so state survives an einhasad-bar restart.
//
// This replaces the bash nohup_to_pidfile + pkill -P + lsof straggler-sweep
// logic from the SwiftBar setup.
package supervisor

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/einhasad/einhasad-bar/internal/config"
	"github.com/einhasad/einhasad-bar/internal/paths"

	"github.com/joho/godotenv"
)

// stopTimeout is how long we wait for a graceful SIGTERM before SIGKILL.
const stopTimeout = 8 * time.Second

// Supervisor tracks the processes einhasad-bar has started.
type Supervisor struct {
	mu      sync.Mutex
	running map[string]*exec.Cmd // key → running command (for in-process reaping)
}

func New() *Supervisor {
	return &Supervisor{running: make(map[string]*exec.Cmd)}
}

func key(projectID, serviceID string) string { return projectID + "/" + serviceID }

// Start launches a process-mode service. It is a no-op if the service is
// already alive (per its pidfile), making it safe to call repeatedly.
func (s *Supervisor) Start(projectID string, svc config.Service) error {
	if svc.Mode != config.ModeProcess {
		return fmt.Errorf("service %q is not a process", svc.ID)
	}
	if s.Alive(projectID, svc.ID) {
		return nil
	}

	logPath, err := paths.LogFile(projectID, svc.ID)
	if err != nil {
		return err
	}
	pidPath, err := paths.PidFile(projectID, svc.ID)
	if err != nil {
		return err
	}

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}

	env, err := buildEnv(svc.WorkingDir, svc.EnvFile, svc.Env)
	if err != nil {
		logFile.Close()
		return err
	}

	cmd := exec.Command(svc.Command, svc.Args...)
	cmd.Dir = svc.WorkingDir
	cmd.Env = env
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Own process group so we can signal the entire child tree.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return err
	}

	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		_ = s.signalGroup(cmd.Process.Pid, syscall.SIGKILL)
		logFile.Close()
		return err
	}

	s.mu.Lock()
	s.running[key(projectID, svc.ID)] = cmd
	s.mu.Unlock()

	// Reap when it exits so we don't leave a zombie, and drop the pidfile.
	go func() {
		_ = cmd.Wait()
		logFile.Close()
		s.mu.Lock()
		delete(s.running, key(projectID, svc.ID))
		s.mu.Unlock()
		// Only remove the pidfile if it still points at this process.
		if pid, ok := readPid(pidPath); ok && pid == cmd.Process.Pid {
			_ = os.Remove(pidPath)
		}
	}()

	return nil
}

// Stop gracefully terminates a service's process group, escalating to SIGKILL
// after stopTimeout, then removes the pidfile.
func (s *Supervisor) Stop(projectID string, svc config.Service) error {
	pidPath, err := paths.PidFile(projectID, svc.ID)
	if err != nil {
		return err
	}
	pid, ok := readPid(pidPath)
	if !ok || !pidAlive(pid) {
		_ = os.Remove(pidPath)
		return nil
	}

	_ = s.signalGroup(pid, syscall.SIGTERM)

	deadline := time.Now().Add(stopTimeout)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			_ = os.Remove(pidPath)
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}

	_ = s.signalGroup(pid, syscall.SIGKILL)
	_ = os.Remove(pidPath)
	return nil
}

// RunLifecycle runs a watch service's Start or Stop command. Output is appended
// to the service's log. Unlike Start/Stop above, no pidfile is written: a watch
// service's health is owned entirely by its check, so einhasad-bar never tracks
// the process it spawned here.
//
// When detach is true (Start), the command runs in its own session (Setsid) so a
// foreground daemon — mysqld, `garage server` — outlives einhasad-bar, and we do
// not wait for it. When false (Stop), it runs to completion bounded by
// stopTimeout, so the next health poll reflects the result.
func (s *Supervisor) RunLifecycle(projectID string, svc config.Service, c *config.Command, detach bool) error {
	if c == nil || c.Command == "" {
		return fmt.Errorf("service %q: no lifecycle command", svc.ID)
	}

	logPath, err := paths.LogFile(projectID, svc.ID)
	if err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}

	env, err := buildEnv(c.WorkingDir, c.EnvFile, c.Env)
	if err != nil {
		logFile.Close()
		return err
	}

	cmd := exec.Command(c.Command, c.Args...)
	cmd.Dir = c.WorkingDir
	cmd.Env = env
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if detach {
		// New session: detach from einhasad-bar's process group so the daemon
		// survives the app quitting (watch semantics — we don't own it).
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			logFile.Close()
			return err
		}
		// Reap asynchronously so a long-lived daemon doesn't leak a zombie and
		// the log file is closed once it exits.
		go func() { _ = cmd.Wait(); logFile.Close() }()
		return nil
	}

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		logFile.Close()
		return err
	case <-time.After(stopTimeout):
		_ = cmd.Process.Kill()
		logFile.Close()
		return fmt.Errorf("service %q: stop command timed out", svc.ID)
	}
}

// RunForeground runs a process-mode service attached to the current terminal:
// stdio is inherited, no log file or pidfile is written, and it stays in this
// process's group so an interactive Ctrl-C reaches the whole child tree
// (make → air → server) — exactly like running the command by hand. It blocks
// until the process exits.
func (s *Supervisor) RunForeground(svc config.Service) error {
	if svc.Mode != config.ModeProcess {
		return fmt.Errorf("service %q is not a process", svc.ID)
	}
	env, err := buildEnv(svc.WorkingDir, svc.EnvFile, svc.Env)
	if err != nil {
		return err
	}
	cmd := exec.Command(svc.Command, svc.Args...)
	cmd.Dir = svc.WorkingDir
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// Intentionally no Setpgid: share the terminal's foreground process group so
	// Ctrl-C signals the whole tree, not just the leader.
	return cmd.Run()
}

// Alive reports whether a managed service's tracked process is running.
func (s *Supervisor) Alive(projectID, serviceID string) bool {
	pidPath, err := paths.PidFile(projectID, serviceID)
	if err != nil {
		return false
	}
	pid, ok := readPid(pidPath)
	return ok && pidAlive(pid)
}

// PID returns the tracked process id for a managed service, if alive.
func (s *Supervisor) PID(projectID, serviceID string) (int, bool) {
	pidPath, err := paths.PidFile(projectID, serviceID)
	if err != nil {
		return 0, false
	}
	pid, ok := readPid(pidPath)
	if !ok || !pidAlive(pid) {
		return 0, false
	}
	return pid, true
}

// signalGroup sends sig to the whole process group led by pid. Setpgid makes the
// group id equal to the leader's pid, so we signal -pid.
func (s *Supervisor) signalGroup(pid int, sig syscall.Signal) error {
	if err := syscall.Kill(-pid, sig); err != nil {
		// Fall back to the lone process if the group is already gone.
		return syscall.Kill(pid, sig)
	}
	return nil
}

// buildEnv layers env_file then the static env map on top of the current
// environment. env_file is parsed as a plain .env (godotenv); dotenvx-encrypted
// files should instead be wrapped at the command level (command: dotenvx …).
func buildEnv(workingDir, envFile string, extra map[string]string) ([]string, error) {
	merged := map[string]string{}

	if envFile != "" {
		path := envFile
		if workingDir != "" && !strings.HasPrefix(path, "/") {
			path = workingDir + "/" + path
		}
		fromFile, err := godotenv.Read(path)
		if err != nil {
			return nil, fmt.Errorf("env_file %s: %w", path, err)
		}
		for k, v := range fromFile {
			merged[k] = v
		}
	}
	for k, v := range extra {
		merged[k] = v
	}

	env := os.Environ()
	for k, v := range merged {
		env = append(env, k+"="+v)
	}
	return env, nil
}

func readPid(pidPath string) (int, bool) {
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// pidAlive reports whether a process exists (signal 0 probes without killing).
func pidAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
