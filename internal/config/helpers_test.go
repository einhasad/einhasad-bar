package config_test

import (
	"os"
	"reflect"
	"testing"
)

// eq fails the test unless got deep-equals want. Branching lives here so test
// bodies stay linear (Arrange/Act/Assert with no conditionals).
func eq(t *testing.T, msg string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: got %v, want %v", msg, got, want)
	}
}

// neq fails the test if got deep-equals want (used to assert a non-nil error).
func neq(t *testing.T, msg string, got, want any) {
	t.Helper()
	if reflect.DeepEqual(got, want) {
		t.Fatalf("%s: got %v, did not want it", msg, got)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}
