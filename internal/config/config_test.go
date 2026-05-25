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

func TestLoadFile_watchRejectsStartStop(t *testing.T) {
	// Watch mode is monitor-only; start/stop commands are forbidden.
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.einhasad-bar.yaml")

	writeFile(t, path, "project:\n  id: p\nservices:\n"+
		"  - id: s\n    mode: watch\n    check: { type: tcp, port: 1 }\n"+
		"    start: { command: server }\n")
	_, err := config.LoadFile(path)
	neq(t, "watch+start is rejected", err, nil)

	writeFile(t, path, "project:\n  id: p\nservices:\n"+
		"  - id: s\n    mode: watch\n    check: { type: tcp, port: 1 }\n"+
		"    stop: { command: server }\n")
	_, err = config.LoadFile(path)
	neq(t, "watch+stop is rejected", err, nil)
}

func TestLoadFile_parsesGroupOnProcessService(t *testing.T) {
	// Group deduplication is mode-agnostic — test with process mode.
	dir := t.TempDir()
	path := filepath.Join(dir, "ev.einhasad-bar.yaml")
	writeFile(t, path, "project:\n  id: ev\nservices:\n"+
		"  - id: api\n    mode: process\n    group: servers\n    command: server\n    args: [run]\n    working_dir: /srv/ev\n")

	proj, err := config.LoadFile(path)

	eq(t, "no error", err, nil)
	eq(t, "group parsed", proj.Services[0].Group, "servers")
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

func TestLoadFile_expandsProjectDir(t *testing.T) {
	// Arrange
	dir := realDir(t)
	path := filepath.Join(dir, "aw.einhasad-bar.yaml")
	writeFile(t, path, "project:\n  id: aw\nservices:\n  - id: s\n    command: echo\n    working_dir: \"$PROJECT_DIR/server\"\n    check: { type: tcp, port: 1 }\n")

	// Act
	proj, err := config.LoadFile(path)

	// Assert
	eq(t, "no error", err, nil)
	eq(t, "PROJECT_DIR expanded in working_dir", proj.Services[0].WorkingDir, dir+"/server")
}

func TestLoadFile_expandsDeclaredVariables(t *testing.T) {
	// Arrange
	dir := realDir(t)
	path := filepath.Join(dir, "aw.einhasad-bar.yaml")
	writeFile(t, path, "project:\n  id: aw\nvariables:\n  BIN: /usr/local/bin\n  DATA: $PROJECT_DIR/data\nservices:\n  - id: s\n    mode: process\n    command: \"$BIN/mysqld\"\n    check: { type: pidfile, pidfile: \"$DATA/s.pid\" }\n")

	// Act
	proj, err := config.LoadFile(path)

	// Assert
	eq(t, "no error", err, nil)
	eq(t, "declared var expanded in command", proj.Services[0].Command, "/usr/local/bin/mysqld")
	eq(t, "var referencing PROJECT_DIR expanded in pidfile", proj.Services[0].Check.Pidfile, dir+"/data/s.pid")
}

func TestLoadFile_mergesLocalSibling(t *testing.T) {
	// Arrange — base declares a variable, local overrides it.
	dir := t.TempDir()
	base := filepath.Join(dir, "ev.einhasad-bar.yaml")
	local := filepath.Join(dir, "ev.einhasad-bar.local.yaml")
	writeFile(t, base, "project:\n  id: ev\nvariables:\n  BIN: /default/bin\nservices:\n  - id: s\n    mode: process\n    command: \"$BIN/server\"\n")
	writeFile(t, local, "variables:\n  BIN: /local/bin\n")

	// Act
	proj, err := config.LoadFile(base)

	// Assert
	eq(t, "no error", err, nil)
	eq(t, "local var override wins", proj.Services[0].Command, "/local/bin/server")
}

func TestLoadFile_localAlwaysWinsOverNonLocal(t *testing.T) {
	// Arrange — three files; local must win even though "z" sorts after "local".
	dir := t.TempDir()
	base := filepath.Join(dir, "p.einhasad-bar.yaml")
	nonLocal := filepath.Join(dir, "p.einhasad-bar.z.yaml")
	local := filepath.Join(dir, "p.einhasad-bar.local.yaml")
	writeFile(t, base, "project:\n  id: p\nvariables:\n  BIN: /base\nservices:\n  - id: s\n    mode: process\n    command: \"$BIN/s\"\n")
	writeFile(t, nonLocal, "variables:\n  BIN: /nonlocal\n")
	writeFile(t, local, "variables:\n  BIN: /local\n")

	// Act
	proj, err := config.LoadFile(base)

	// Assert
	eq(t, "no error", err, nil)
	eq(t, "local.yaml wins over non-local regardless of sort order", proj.Services[0].Command, "/local/s")
}

func TestLoadFile_mergesServicesByID(t *testing.T) {
	// Arrange — local overrides a single service field without touching others.
	dir := t.TempDir()
	base := filepath.Join(dir, "p.einhasad-bar.yaml")
	local := filepath.Join(dir, "p.einhasad-bar.local.yaml")
	writeFile(t, base, "project:\n  id: p\nservices:\n  - id: api\n    command: make\n    args: [run]\n    working_dir: /base/server\n    check: { type: tcp, port: 4444 }\n")
	writeFile(t, local, "services:\n  - id: api\n    working_dir: /local/server\n")

	// Act
	proj, err := config.LoadFile(base)

	// Assert
	eq(t, "no error", err, nil)
	eq(t, "local working_dir wins", proj.Services[0].WorkingDir, "/local/server")
	eq(t, "base command preserved", proj.Services[0].Command, "make")
}

func TestLoadFile_include(t *testing.T) {
	// Arrange — root file includes a sibling file that adds a service.
	root := realDir(t)
	sub := t.TempDir()
	subReal, _ := filepath.EvalSymlinks(sub)

	rootFile := filepath.Join(root, "ev.einhasad-bar.yaml")
	subFile := filepath.Join(subReal, "backend.yaml")

	writeFile(t, rootFile, "project:\n  id: ev\ninclude:\n  - "+subFile+"\nservices:\n  - id: nginx\n    mode: watch\n    check: { type: tcp, port: 80 }\n")
	writeFile(t, subFile, "services:\n  - id: mysql\n    mode: watch\n    check: { type: tcp, port: 3306 }\n")

	// Act
	proj, err := config.LoadFile(rootFile)

	// Assert
	eq(t, "no error", err, nil)
	eq(t, "service count", len(proj.Services), 2)
	eq(t, "first service id", proj.Services[0].ID, "nginx")
	eq(t, "included service id", proj.Services[1].ID, "mysql")
}

func TestLoadFile_includeProjectDirIsOwnDir(t *testing.T) {
	// Arrange — the included file uses its own $PROJECT_DIR (not the root's).
	root := realDir(t)
	sub := realDir(t)

	rootFile := filepath.Join(root, "ev.einhasad-bar.yaml")
	subFile := filepath.Join(sub, "backend.yaml")

	writeFile(t, rootFile, "project:\n  id: ev\ninclude:\n  - "+subFile+"\nservices:\n  - id: nginx\n    mode: watch\n    check: { type: tcp, port: 80 }\n")
	writeFile(t, subFile, "services:\n  - id: mysql\n    mode: watch\n    check: { type: pidfile, pidfile: \"$PROJECT_DIR/run/mysqld.pid\" }\n")

	// Act
	proj, err := config.LoadFile(rootFile)

	// Assert
	eq(t, "no error", err, nil)
	var mysql config.Service
	for _, s := range proj.Services {
		if s.ID == "mysql" {
			mysql = s
		}
	}
	eq(t, "included $PROJECT_DIR = included file's dir", mysql.Check.Pidfile, sub+"/run/mysqld.pid")
}

func TestLoadFiles_mergesMultipleFiles(t *testing.T) {
	// Arrange
	dir := realDir(t)
	f1 := filepath.Join(dir, "a.yaml")
	f2 := filepath.Join(dir, "b.yaml")
	writeFile(t, f1, "project:\n  id: ev\nservices:\n  - id: nginx\n    mode: watch\n    check: { type: tcp, port: 80 }\n")
	writeFile(t, f2, "project:\n  id: ev\nservices:\n  - id: mysql\n    mode: watch\n    check: { type: tcp, port: 3306 }\n")

	// Act
	proj, err := config.LoadFiles([]string{f1, f2})

	// Assert
	eq(t, "no error", err, nil)
	eq(t, "service count", len(proj.Services), 2)
}

func TestLoadFiles_rejectsMismatchedIDs(t *testing.T) {
	// Arrange
	dir := realDir(t)
	f1 := filepath.Join(dir, "a.yaml")
	f2 := filepath.Join(dir, "b.yaml")
	writeFile(t, f1, "project:\n  id: ev\nservices:\n  - id: nginx\n    mode: watch\n    check: { type: tcp, port: 80 }\n")
	writeFile(t, f2, "project:\n  id: other\nservices:\n  - id: mysql\n    mode: watch\n    check: { type: tcp, port: 3306 }\n")

	// Act
	_, err := config.LoadFiles([]string{f1, f2})

	// Assert
	neq(t, "mismatched IDs are rejected", err, nil)
}
