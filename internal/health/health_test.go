package health_test

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/einhasad/einhasad-bar/internal/config"
	"github.com/einhasad/einhasad-bar/internal/health"
)

func TestProbe_httpOK(t *testing.T) {
	// Arrange
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	check := &config.Check{Type: config.CheckHTTP, URL: srv.URL}

	// Act
	ok := health.Probe(check)

	// Assert
	assertTrue(t, "http 200 probe passes", ok)
}

func TestProbe_httpUnreachable(t *testing.T) {
	// Arrange — a server we immediately close, so the port is dead.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	check := &config.Check{Type: config.CheckHTTP, URL: url}

	// Act
	ok := health.Probe(check)

	// Assert
	assertFalse(t, "http probe to closed server fails", ok)
}

func TestProbe_tcpOK(t *testing.T) {
	// Arrange — listen on an ephemeral port, then probe it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	assertNil(t, "listener opens", err)
	defer ln.Close()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	check := &config.Check{Type: config.CheckTCP, Port: port}

	// Act
	ok := health.Probe(check)

	// Assert
	assertTrue(t, "tcp probe to open port passes", ok)
}

func TestProbe_tcpClosedPort(t *testing.T) {
	// Arrange — open then close a listener to obtain a definitely-closed port.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	ln.Close()
	check := &config.Check{Type: config.CheckTCP, Port: port}

	// Act
	ok := health.Probe(check)

	// Assert
	assertFalse(t, "tcp probe to closed port fails", ok)
}

// --- assert helpers keep branching out of the test bodies ---

func assertTrue(t *testing.T, msg string, got bool) {
	t.Helper()
	if !got {
		t.Fatalf("%s: got false, want true", msg)
	}
}

func assertFalse(t *testing.T, msg string, got bool) {
	t.Helper()
	if got {
		t.Fatalf("%s: got true, want false", msg)
	}
}

func assertNil(t *testing.T, msg string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", msg, err)
	}
}
