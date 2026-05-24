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
	if c == nil {
		return false
	}
	switch c.Type {
	case config.CheckTCP:
		return tcpOK(c.Port)
	case config.CheckHTTP:
		return httpOK(c.URL)
	case config.CheckPidfile:
		return pidfileOK(c.Pidfile)
	default:
		return false
	}
}

func tcpOK(port int) bool {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, probeTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func httpOK(url string) bool {
	client := &http.Client{Timeout: probeTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}

func pidfileOK(path string) bool {
	if path == "" {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
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
