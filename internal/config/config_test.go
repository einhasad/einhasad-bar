package config_test

import (
	"path/filepath"
	"testing"

	"github.com/einhasad/einhasad-bar/internal/config"
)

// awFixture is a self-contained stand-in for a real project file. Tests use
// fixtures rather than the per-project einhasad-bar.yaml files (which live in
// each project's own repo) so the suite stays hermetic.
const awFixture = `project:
  name: "AlmaWord"
  id: "aw"
services:
  - id: mysql
    label: "MySQL"
    mode: watch
    check: { type: tcp, port: 3306 }
    start: { command: mysqld, args: ["--defaults-file", "/tmp/my.cnf"] }
    stop:  { command: mysqladmin, args: ["shutdown"] }
  - id: garage
    mode: watch
    check: { type: http, url: "http://localhost:3903/health" }
  - id: grpc_server
    label: "gRPC API"
    command: make
    args: ["run"]
    working_dir: "/srv/almaword/server"
    check: { type: tcp, port: 4444 }
`

func TestLoadDir_loadsSortedByFilename(t *testing.T) {
	// Arrange — two fixtures whose filenames sort a-before-b.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.einhasad-bar.yaml"), "project:\n  id: aaa\nservices:\n  - id: x\n    mode: watch\n    check: { type: tcp, port: 1 }\n")
	writeFile(t, filepath.Join(dir, "b.einhasad-bar.yaml"), "project:\n  id: bbb\nservices:\n  - id: y\n    mode: watch\n    check: { type: tcp, port: 2 }\n")

	// Act
	projects, err := config.LoadDir(dir)

	// Assert
	eq(t, "no load error", err, nil)
	eq(t, "two projects discovered", len(projects), 2)
	eq(t, "sorted by filename: aaa first", projects[0].ID, "aaa")
	eq(t, "sorted by filename: bbb second", projects[1].ID, "bbb")
}

func TestLoadFile_appliesDefaultsAndModes(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	path := filepath.Join(dir, "aw.einhasad-bar.yaml")
	writeFile(t, path, awFixture)

	// Act
	proj, err := config.LoadFile(path)

	// Assert
	eq(t, "no error", err, nil)
	eq(t, "project name parsed", proj.Name, "AlmaWord")
	eq(t, "service count", len(proj.Services), 3)

	mysql := proj.Services[0]
	eq(t, "mysql is watch mode", mysql.Mode, config.ModeWatch)
	eq(t, "mysql required defaults true", mysql.IsRequired(), true)
	eq(t, "mysql tcp port", mysql.Check.Port, 3306)

	grpc := proj.Services[2]
	eq(t, "grpc inferred as process mode", grpc.Mode, config.ModeProcess)
	eq(t, "grpc working dir parsed", grpc.WorkingDir, "/srv/almaword/server")
}

func TestLoadFile_parsesWatchLifecycleAndGroup(t *testing.T) {
	// Arrange — a watch service carrying start/stop + a group.
	dir := t.TempDir()
	path := filepath.Join(dir, "ev.einhasad-bar.yaml")
	writeFile(t, path, "project:\n  id: ev\nservices:\n"+
		"  - id: mysql\n    mode: watch\n    group: lamp\n    check: { type: tcp, port: 3306 }\n"+
		"    start: { command: server, args: [up], working_dir: /srv/ev }\n"+
		"    stop:  { command: server, args: [down], working_dir: /srv/ev }\n")

	// Act
	proj, err := config.LoadFile(path)

	// Assert
	eq(t, "no error", err, nil)
	svc := proj.Services[0]
	eq(t, "group parsed", svc.Group, "lamp")
	eq(t, "start command parsed", svc.Start.Command, "server")
	eq(t, "start args parsed", svc.Start.Args, []string{"up"})
	eq(t, "stop working_dir parsed", svc.Stop.WorkingDir, "/srv/ev")
}

func TestLoadFile_rejectsUnknownMode(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.einhasad-bar.yaml")
	writeFile(t, path, "project:\n  id: bad\nservices:\n  - id: x\n    mode: rocket\n    command: echo\n")

	// Act
	_, err := config.LoadFile(path)

	// Assert
	neq(t, "unknown mode is rejected", err, nil)
}

func TestLoadFile_watchRequiresCheck(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.einhasad-bar.yaml")
	writeFile(t, path, "project:\n  id: bad\nservices:\n  - id: x\n    mode: watch\n")

	// Act
	_, err := config.LoadFile(path)

	// Assert
	neq(t, "watch without check is rejected", err, nil)
}

func TestLoadFile_rejectsEmptyLifecycleCommand(t *testing.T) {
	// Arrange — a start stanza present but without a command.
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.einhasad-bar.yaml")
	writeFile(t, path, "project:\n  id: bad\nservices:\n  - id: x\n    mode: watch\n    check: { type: tcp, port: 1 }\n    start: { args: [up] }\n")

	// Act
	_, err := config.LoadFile(path)

	// Assert
	neq(t, "empty start command is rejected", err, nil)
}
