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
	Services []Service `yaml:"services"`
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

// LoadFile parses and validates a single config file.
func LoadFile(path string) (Project, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Project{}, err
	}

	var raw rawConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return Project{}, err
	}

	proj := Project{
		Name:       raw.Project.Name,
		ID:         raw.Project.ID,
		Services:   raw.Services,
		SourceFile: path,
	}
	if proj.ID == "" {
		return Project{}, fmt.Errorf("project.id is required")
	}
	if proj.Name == "" {
		proj.Name = proj.ID
	}

	for i := range proj.Services {
		if err := normalizeService(&proj.Services[i]); err != nil {
			return Project{}, err
		}
	}
	return proj, nil
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
