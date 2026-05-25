// Package health runs the readiness probes declared in a service's check
// stanza: tcp (dial a port), http (expect a 2xx), or pidfile (a tracked pid is
// alive). It is deliberately stateless — the caller decides what a passing probe
// means for a given service mode.
package health

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/einhasad/einhasad-bar/internal/config"
)

// probeTimeout is kept below the UI poll interval so a single hung probe can't
// stall the refresh loop. Local checks normally resolve far faster (an accept or
// a connection-refused), so this only bounds the pathological hang case.
const probeTimeout = 1 * time.Second

// Probe runs a single check and reports whether it passed.
func Probe(c *config.Check) bool {
	ok, _ := ProbeReason(c)
	return ok
}

// ProbeReason runs a single check and returns whether it passed plus a
// human-readable reason string (empty on success).
func ProbeReason(c *config.Check) (bool, string) {
	if c == nil {
		return false, "no check configured"
	}
	switch c.Type {
	case config.CheckTCP:
		return probeTCP(c.Port)
	case config.CheckHTTP:
		return probeHTTP(c.URL)
	case config.CheckPidfile:
		return probePidfile(c.Pidfile)
	default:
		return false, fmt.Sprintf("unknown check type %q", c.Type)
	}
}

func probeTCP(port int) (bool, string) {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, probeTimeout)
	if err != nil {
		if strings.Contains(err.Error(), "refused") {
			return false, fmt.Sprintf("port %d: connection refused", port)
		}
		return false, fmt.Sprintf("port %d: %v", port, err)
	}
	_ = conn.Close()
	return true, ""
}

func probeHTTP(url string) (bool, string) {
	client := &http.Client{Timeout: probeTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return false, fmt.Sprintf("HTTP %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return false, fmt.Sprintf("HTTP %s: status %d", url, resp.StatusCode)
	}
	return true, ""
}

func probePidfile(path string) (bool, string) {
	if path == "" {
		return false, "no pidfile path configured"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Sprintf("no pidfile at %s", path)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return false, fmt.Sprintf("invalid pid in %s", path)
	}
	if syscall.Kill(pid, 0) != nil {
		return false, fmt.Sprintf("process %d is not running", pid)
	}
	return true, ""
}

// String renders a check for the details/log output.
func String(c *config.Check) string {
	if c == nil {
		return "none"
	}
	switch c.Type {
	case config.CheckTCP:
		return fmt.Sprintf("tcp:%d", c.Port)
	case config.CheckHTTP:
		return "http:" + c.URL
	case config.CheckPidfile:
		return "pidfile:" + c.Pidfile
	default:
		return string(c.Type)
	}
}
