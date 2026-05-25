// Package config loads docker-compose-style *.einhasad-bar.yaml stack
// definitions. Each file describes one project (AlmaWord, Elite Vehicle, …) and
// its services. The schema is intentionally small for the MVP: process/watch
// modes and tcp/http/pidfile checks. Unknown fields are ignored so the schema
// can grow (install hooks, launchd, groups) without breaking older files.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Mode describes how einhasad-bar relates to a service.
type Mode string

const (
	// ModeProcess: einhasad-bar spawns and kills the service itself.
	ModeProcess Mode = "process"
	// ModeWatch: einhasad-bar only monitors an externally-managed daemon.
	ModeWatch Mode = "watch"
)

// CheckType is the kind of health probe used for a service.
type CheckType string

const (
	CheckTCP     CheckType = "tcp"
	CheckHTTP    CheckType = "http"
	CheckPidfile CheckType = "pidfile"
)

// Check is a single health probe.
type Check struct {
	Type    CheckType `yaml:"type"`
	Port    int       `yaml:"port"`
	URL     string    `yaml:"url"`
	Pidfile string    `yaml:"pidfile"`
}

// Command describes how to run a one-shot lifecycle action — the Start/Stop of a
// watch service (an externally-managed daemon like mysqld, or a delegate script
// like EV's `dev/local/server up`). It mirrors the process-spawn fields but is
// run to-completion (stop) or detached (start) rather than supervised, since a
// watch service's health is owned entirely by its check, not a pidfile.
type Command struct {
	Command    string            `yaml:"command"`
	Args       []string          `yaml:"args"`
	WorkingDir string            `yaml:"working_dir"`
	EnvFile    string            `yaml:"env_file"`
	Env        map[string]string `yaml:"env"`
}

// Service is one managed or watched process within a project.
type Service struct {
	ID         string            `yaml:"id"`
	Label      string            `yaml:"label"`
	Mode       Mode              `yaml:"mode"`
	Required   *bool             `yaml:"required"` // nil → defaults to true
	Command    string            `yaml:"command"`
	Args       []string          `yaml:"args"`
	WorkingDir string            `yaml:"working_dir"`
	EnvFile    string            `yaml:"env_file"`
	Env        map[string]string `yaml:"env"`
	URL        string            `yaml:"url"`
	Check      *Check            `yaml:"check"`
	// Start/Stop give a watch service lifecycle control without making
	// einhasad-bar own the process: Start runs detached (the daemon outlives the
	// app), Stop runs to completion. Health still comes from Check alone.
	Start *Command `yaml:"start"`
	Stop  *Command `yaml:"stop"`
	// Group deduplicates shared lifecycle commands: services in the same group
	// fire Start/Stop once during "Start server" (e.g. EV's five LAMP services
	// all delegate to one `dev/local/server up`).
	Group string `yaml:"group"`
}

// IsRequired reports whether the service gates the project's status icon.
// Defaults to true when unspecified.
func (s Service) IsRequired() bool { return s.Required == nil || *s.Required }

// Project is one loaded *.einhasad-bar.yaml file.
type Project struct {
	Name       string
	ID         string
	Services   []Service
	SourceFile string
}

type rawConfig struct {
	Project struct {
		Name string `yaml:"name"`
		ID   string `yaml:"id"`
	} `yaml:"project"`
	Include   []string          `yaml:"include"`
	Variables map[string]string `yaml:"variables"`
	Services  []Service         `yaml:"services"`
}

// Glob is the filename pattern einhasad-bar looks for.
const Glob = "*.einhasad-bar.yaml"

// LoadDir reads every *.einhasad-bar.yaml in dir, validates it, applies
// defaults, and returns the projects sorted by filename for a stable menu order.
func LoadDir(dir string) ([]Project, error) {
	matches, err := filepath.Glob(filepath.Join(dir, Glob))
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)

	var projects []Project
	for _, path := range matches {
		p, err := LoadFile(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		projects = append(projects, p)
	}
	return projects, nil
}

// LoadFile parses the base file and any sibling <base>.*.yaml files in the same
// real directory (symlinks are resolved). Siblings are merged in sort order with
// *.local.yaml files always loaded last so they can override anything.
// Variable expansion ($PROJECT_DIR, declared variables, env vars, ~) happens
// after the merge so all files share one resolved namespace.
// project.id is required; use LoadFiles for multi-file merges where only the
// combined result needs an id.
func LoadFile(path string) (Project, error) {
	return loadFile(path, true)
}

// LoadFiles loads one or more config files and merges their services into a
// single Project. All files that declare a project.id must declare the same
// one; at least one file must supply an id.
func LoadFiles(filePaths []string) (Project, error) {
	if len(filePaths) == 0 {
		return Project{}, fmt.Errorf("no config files specified")
	}
	var merged Project
	for i, path := range filePaths {
		p, err := loadFile(path, false)
		if err != nil {
			return Project{}, fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		if i == 0 {
			merged = p
		} else {
			if merged.ID != "" && p.ID != "" && merged.ID != p.ID {
				return Project{}, fmt.Errorf("project ID mismatch: %q (%s) vs %q (%s)",
					merged.ID, filepath.Base(filePaths[0]), p.ID, filepath.Base(path))
			}
			if p.ID != "" {
				merged.ID = p.ID
			}
			if p.Name != "" && merged.Name == merged.ID {
				merged.Name = p.Name
			}
			merged.Services = mergeProjectServices(merged.Services, p.Services)
		}
	}
	if merged.ID == "" {
		return Project{}, fmt.Errorf("project.id is required; declare it in at least one of the specified files")
	}
	return merged, nil
}

// mergeProjectServices merges src into dst by service ID, same strategy as
// mergeRaw but operating on already-expanded Service values.
func mergeProjectServices(dst, src []Service) []Service {
	for _, svc := range src {
		found := false
		for i, existing := range dst {
			if existing.ID == svc.ID {
				dst[i] = mergeService(existing, svc)
				found = true
				break
			}
		}
		if !found {
			dst = append(dst, svc)
		}
	}
	return dst
}

// loadFile is the internal implementation. requireID=false allows an empty
// project.id, used when loading files that will be merged with others.
func loadFile(path string, requireID bool) (Project, error) {
	// Resolve symlink so PROJECT_DIR points to the real project directory.
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		realPath = path
	}

	dir := filepath.Dir(realPath)
	base := filepath.Base(realPath)
	prefix := strings.TrimSuffix(base, ".yaml")

	siblings, _ := filepath.Glob(filepath.Join(dir, prefix+".*.yaml"))
	sort.Strings(siblings)

	// Non-local siblings load before *.local.yaml so local always wins.
	var nonLocal, local []string
	for _, s := range siblings {
		if strings.HasSuffix(filepath.Base(s), ".local.yaml") {
			local = append(local, s)
		} else {
			nonLocal = append(nonLocal, s)
		}
	}

	filePaths := make([]string, 0, 1+len(nonLocal)+len(local))
	filePaths = append(filePaths, realPath)
	filePaths = append(filePaths, nonLocal...)
	filePaths = append(filePaths, local...)

	var merged rawConfig
	for _, p := range filePaths {
		raw, err := parseRaw(p)
		if err != nil {
			return Project{}, fmt.Errorf("%s: %w", filepath.Base(p), err)
		}
		merged = mergeRaw(merged, raw)
	}

	expand := buildExpander(merged.Variables, dir)

	merged.Project.Name = expand(merged.Project.Name)
	merged.Project.ID = expand(merged.Project.ID)
	for i := range merged.Services {
		expandService(&merged.Services[i], expand)
	}

	proj := Project{
		Name:       merged.Project.Name,
		ID:         merged.Project.ID,
		Services:   merged.Services,
		SourceFile: realPath,
	}
	if requireID && proj.ID == "" {
		return Project{}, fmt.Errorf("project.id is required")
	}
	if proj.Name == "" {
		proj.Name = proj.ID
	}

	// Process include: directives. Each included file loads with its own
	// $PROJECT_DIR; their services are merged in declaration order.
	for _, inclRaw := range merged.Include {
		inclPath := expand(inclRaw)
		if !filepath.IsAbs(inclPath) {
			inclPath = filepath.Join(dir, inclPath)
		}
		included, err := loadFile(inclPath, false)
		if err != nil {
			return Project{}, fmt.Errorf("include %q: %w", inclRaw, err)
		}
		if included.ID != "" && proj.ID != "" && included.ID != proj.ID {
			return Project{}, fmt.Errorf("include %q: project.id %q does not match %q", inclRaw, included.ID, proj.ID)
		}
		proj.Services = mergeProjectServices(proj.Services, included.Services)
	}

	for i := range proj.Services {
		if err := normalizeService(&proj.Services[i]); err != nil {
			return Project{}, err
		}
	}
	return proj, nil
}

func parseRaw(path string) (rawConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return rawConfig{}, err
	}
	var raw rawConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return rawConfig{}, err
	}
	return raw, nil
}

// mergeRaw overlays src onto dst: later files win for scalars, maps are merged,
// services are merged by ID.
func mergeRaw(dst, src rawConfig) rawConfig {
	if src.Project.Name != "" {
		dst.Project.Name = src.Project.Name
	}
	if src.Project.ID != "" {
		dst.Project.ID = src.Project.ID
	}

	if len(src.Include) > 0 {
		dst.Include = append(dst.Include, src.Include...)
	}

	if len(src.Variables) > 0 {
		if dst.Variables == nil {
			dst.Variables = make(map[string]string)
		}
		for k, v := range src.Variables {
			dst.Variables[k] = v
		}
	}

	for _, svc := range src.Services {
		found := false
		for i, existing := range dst.Services {
			if existing.ID == svc.ID {
				dst.Services[i] = mergeService(existing, svc)
				found = true
				break
			}
		}
		if !found {
			dst.Services = append(dst.Services, svc)
		}
	}

	return dst
}

func mergeService(dst, src Service) Service {
	if src.Label != "" {
		dst.Label = src.Label
	}
	if src.Mode != "" {
		dst.Mode = src.Mode
	}
	if src.Required != nil {
		dst.Required = src.Required
	}
	if src.Command != "" {
		dst.Command = src.Command
	}
	if src.Args != nil {
		dst.Args = src.Args
	}
	if src.WorkingDir != "" {
		dst.WorkingDir = src.WorkingDir
	}
	if src.EnvFile != "" {
		dst.EnvFile = src.EnvFile
	}
	if src.URL != "" {
		dst.URL = src.URL
	}
	if src.Check != nil {
		dst.Check = src.Check
	}
	if src.Start != nil {
		dst.Start = src.Start
	}
	if src.Stop != nil {
		dst.Stop = src.Stop
	}
	if src.Group != "" {
		dst.Group = src.Group
	}
	if len(src.Env) > 0 {
		if dst.Env == nil {
			dst.Env = make(map[string]string)
		}
		for k, v := range src.Env {
			dst.Env[k] = v
		}
	}
	return dst
}

// buildExpander returns a function that expands $PROJECT_DIR, declared
// variables, env vars, and leading ~ in any string. Variable values themselves
// are expanded using PROJECT_DIR and env only (no inter-variable references).
func buildExpander(vars map[string]string, projectDir string) func(string) string {
	home, _ := os.UserHomeDir()

	// Expand each declared variable value using built-ins + env only.
	baseExpand := func(s string) string {
		if s == "~" {
			return home
		}
		if strings.HasPrefix(s, "~/") {
			s = home + s[1:]
		}
		return os.Expand(s, func(key string) string {
			if key == "PROJECT_DIR" {
				return projectDir
			}
			return os.Getenv(key)
		})
	}

	expanded := make(map[string]string, len(vars))
	for k, v := range vars {
		expanded[k] = baseExpand(v)
	}

	return func(s string) string {
		if s == "~" {
			return home
		}
		if strings.HasPrefix(s, "~/") {
			s = home + s[1:]
		}
		return os.Expand(s, func(key string) string {
			if key == "PROJECT_DIR" {
				return projectDir
			}
			if v, ok := expanded[key]; ok {
				return v
			}
			return os.Getenv(key)
		})
	}
}

func expandService(s *Service, expand func(string) string) {
	s.Command = expand(s.Command)
	for i, a := range s.Args {
		s.Args[i] = expand(a)
	}
	s.WorkingDir = expand(s.WorkingDir)
	s.EnvFile = expand(s.EnvFile)
	s.URL = expand(s.URL)
	for k, v := range s.Env {
		s.Env[k] = expand(v)
	}
	if s.Check != nil {
		expandCheck(s.Check, expand)
	}
	if s.Start != nil {
		expandCommand(s.Start, expand)
	}
	if s.Stop != nil {
		expandCommand(s.Stop, expand)
	}
}

func expandCommand(c *Command, expand func(string) string) {
	c.Command = expand(c.Command)
	for i, a := range c.Args {
		c.Args[i] = expand(a)
	}
	c.WorkingDir = expand(c.WorkingDir)
	c.EnvFile = expand(c.EnvFile)
	for k, v := range c.Env {
		c.Env[k] = expand(v)
	}
}

func expandCheck(c *Check, expand func(string) string) {
	c.URL = expand(c.URL)
	c.Pidfile = expand(c.Pidfile)
}

// normalizeService fills defaults and validates a single service in place.
func normalizeService(s *Service) error {
	if s.ID == "" {
		return fmt.Errorf("service is missing an id")
	}
	if s.Label == "" {
		s.Label = s.ID
	}

	// Infer mode when omitted: a command implies a managed process.
	if s.Mode == "" {
		if s.Command != "" {
			s.Mode = ModeProcess
		} else {
			s.Mode = ModeWatch
		}
	}

	switch s.Mode {
	case ModeProcess:
		if s.Command == "" {
			return fmt.Errorf("service %q: process mode requires a command", s.ID)
		}
	case ModeWatch:
		if s.Check == nil {
			return fmt.Errorf("service %q: watch mode requires a check", s.ID)
		}
		if s.Start != nil {
			return fmt.Errorf("service %q: watch mode cannot have a start command (use process mode to manage the lifecycle)", s.ID)
		}
		if s.Stop != nil {
			return fmt.Errorf("service %q: watch mode cannot have a stop command (use process mode to manage the lifecycle)", s.ID)
		}
	default:
		return fmt.Errorf("service %q: unknown mode %q", s.ID, s.Mode)
	}

	if s.Check != nil {
		if err := validateCheck(s.ID, s.Check); err != nil {
			return err
		}
	}
	if err := validateCommand(s.ID, "start", s.Start); err != nil {
		return err
	}
	if err := validateCommand(s.ID, "stop", s.Stop); err != nil {
		return err
	}
	return nil
}

// validateCommand rejects a declared-but-empty lifecycle command.
func validateCommand(svcID, which string, c *Command) error {
	if c != nil && c.Command == "" {
		return fmt.Errorf("service %q: %s requires a command", svcID, which)
	}
	return nil
}

func validateCheck(svcID string, c *Check) error {
	switch c.Type {
	case CheckTCP:
		if c.Port == 0 {
			return fmt.Errorf("service %q: tcp check requires a port", svcID)
		}
	case CheckHTTP:
		if c.URL == "" {
			return fmt.Errorf("service %q: http check requires a url", svcID)
		}
	case CheckPidfile:
		// pidfile may be empty → defaults to the supervisor's own pidfile.
	default:
		return fmt.Errorf("service %q: unknown check type %q", svcID, c.Type)
	}
	return nil
}
