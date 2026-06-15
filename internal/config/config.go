// Package config loads, expands and validates the agent's YAML configuration.
// Environment variables of the form ${VAR} are expanded via os.ExpandEnv after
// the file is read, so secrets stay out of the YAML.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the root configuration object.
type Config struct {
	Agent    AgentConfig    `yaml:"agent"`
	Profile  ProfileConfig  `yaml:"profile"`
	Sources  []SourceConfig `yaml:"sources"`
	Matching MatchingConfig `yaml:"matching"`
	Notifier NotifierConfig `yaml:"notifier"`
	Database DatabaseConfig `yaml:"database"`
}

// AgentConfig controls the orchestration loop.
type AgentConfig struct {
	Interval      time.Duration `yaml:"interval"`
	MaxConcurrent int           `yaml:"max_concurrent"`
	DryRun        bool          `yaml:"dry_run"`
	LogLevel      string        `yaml:"log_level"`
}

// ProfileConfig is the user's profile used for matching and form filling.
type ProfileConfig struct {
	FullName        string   `yaml:"full_name"`
	Email           string   `yaml:"email"`
	Phone           string   `yaml:"phone"`
	LinkedInURL     string   `yaml:"linkedin_url"`
	CVPath          string   `yaml:"cv_path"`
	TargetRoles     []string `yaml:"target_roles"`
	Skills          []string `yaml:"skills"`
	YearsExperience int      `yaml:"years_experience"`
	Summary         string   `yaml:"summary"`
}

// SourceConfig describes a single scraper source.
type SourceConfig struct {
	Name    string            `yaml:"name"`
	Type    string            `yaml:"type"` // jsearch, career_page
	Enabled bool              `yaml:"enabled"`
	Extra   map[string]string `yaml:"extra"`
}

// MatchingConfig configures the Claude-backed matcher.
type MatchingConfig struct {
	Provider        string  `yaml:"provider"`
	APIKey          string  `yaml:"api_key"`
	Model           string  `yaml:"model"`
	CoverLetterModel string `yaml:"cover_letter_model"`
	MinScore        float64 `yaml:"min_score"`
	AutoApplyAbove  float64 `yaml:"auto_apply_above"`
}

// NotifierConfig configures notifications.
type NotifierConfig struct {
	Type   string            `yaml:"type"` // telegram
	Config map[string]string `yaml:"config"`
}

// DatabaseConfig holds the Postgres DSN.
type DatabaseConfig struct {
	DSN string `yaml:"dsn"`
}

// Load reads, expands env vars in, parses and validates the config at path.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}

	expanded := os.ExpandEnv(string(raw))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	cfg.applyDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return &cfg, nil
}

// applyDefaults fills zero-valued fields with sensible defaults.
func (c *Config) applyDefaults() {
	if c.Agent.Interval == 0 {
		c.Agent.Interval = 4 * time.Hour
	}
	if c.Agent.MaxConcurrent == 0 {
		c.Agent.MaxConcurrent = 3
	}
	if c.Agent.LogLevel == "" {
		c.Agent.LogLevel = "info"
	}
	if c.Matching.Model == "" {
		c.Matching.Model = "claude-haiku-4-5-20251001"
	}
	if c.Matching.CoverLetterModel == "" {
		c.Matching.CoverLetterModel = "claude-sonnet-4-6"
	}
	if c.Matching.MinScore == 0 {
		c.Matching.MinScore = 0.6
	}
	if c.Matching.AutoApplyAbove == 0 {
		c.Matching.AutoApplyAbove = 0.85
	}
	if c.Matching.Provider == "" {
		c.Matching.Provider = "anthropic"
	}
}

// Validate checks the config for the invariants the agent relies on.
func (c *Config) Validate() error {
	var errs []string

	if c.Agent.Interval <= 0 {
		errs = append(errs, "agent.interval must be positive")
	}
	if c.Agent.MaxConcurrent < 1 {
		errs = append(errs, "agent.max_concurrent must be >= 1")
	}
	if !validLogLevel(c.Agent.LogLevel) {
		errs = append(errs, fmt.Sprintf("agent.log_level %q is invalid (debug|info|warn|error)", c.Agent.LogLevel))
	}

	if c.Profile.Email == "" {
		errs = append(errs, "profile.email is required")
	}
	if c.Profile.FullName == "" {
		errs = append(errs, "profile.full_name is required")
	}

	if c.Database.DSN == "" {
		errs = append(errs, "database.dsn is required")
	}

	if c.Matching.MinScore < 0 || c.Matching.MinScore > 1 {
		errs = append(errs, "matching.min_score must be between 0 and 1")
	}
	if c.Matching.AutoApplyAbove < 0 || c.Matching.AutoApplyAbove > 1 {
		errs = append(errs, "matching.auto_apply_above must be between 0 and 1")
	}
	if c.Matching.AutoApplyAbove < c.Matching.MinScore {
		errs = append(errs, "matching.auto_apply_above must be >= matching.min_score")
	}
	if c.Matching.APIKey == "" {
		errs = append(errs, "matching.api_key is required")
	}

	enabled := 0
	seen := map[string]bool{}
	for i, s := range c.Sources {
		if s.Name == "" {
			errs = append(errs, fmt.Sprintf("sources[%d].name is required", i))
			continue
		}
		if seen[s.Name] {
			errs = append(errs, fmt.Sprintf("duplicate source name %q", s.Name))
		}
		seen[s.Name] = true
		if s.Type == "" {
			errs = append(errs, fmt.Sprintf("sources[%q].type is required", s.Name))
		}
		if s.Enabled {
			enabled++
		}
	}
	if enabled == 0 {
		errs = append(errs, "at least one source must be enabled")
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func validLogLevel(l string) bool {
	switch strings.ToLower(l) {
	case "debug", "info", "warn", "error":
		return true
	default:
		return false
	}
}

// EnabledSources returns only the sources with enabled=true.
func (c *Config) EnabledSources() []SourceConfig {
	var out []SourceConfig
	for _, s := range c.Sources {
		if s.Enabled {
			out = append(out, s)
		}
	}
	return out
}
