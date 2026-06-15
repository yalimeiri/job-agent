package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// baseValid returns a Config that passes Validate, so individual tests can
// mutate one field to assert the corresponding failure.
func baseValid() *Config {
	c := &Config{
		Agent: AgentConfig{Interval: time.Hour, MaxConcurrent: 2, LogLevel: "info"},
		Profile: ProfileConfig{FullName: "Jane Dev", Email: "jane@example.com"},
		Sources: []SourceConfig{{Name: "s1", Type: "jsearch", Enabled: true}},
		Matching: MatchingConfig{
			APIKey: "sk-test", MinScore: 0.6, AutoApplyAbove: 0.85,
		},
		Database: DatabaseConfig{DSN: "postgres://localhost/db"},
	}
	return c
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"valid", func(*Config) {}, false},
		{"missing email", func(c *Config) { c.Profile.Email = "" }, true},
		{"missing full name", func(c *Config) { c.Profile.FullName = "" }, true},
		{"missing dsn", func(c *Config) { c.Database.DSN = "" }, true},
		{"missing api key", func(c *Config) { c.Matching.APIKey = "" }, true},
		{"min score too high", func(c *Config) { c.Matching.MinScore = 1.5 }, true},
		{"min score negative", func(c *Config) { c.Matching.MinScore = -0.1 }, true},
		{"auto apply below min", func(c *Config) { c.Matching.AutoApplyAbove = 0.5; c.Matching.MinScore = 0.6 }, true},
		{"bad log level", func(c *Config) { c.Agent.LogLevel = "verbose" }, true},
		{"zero concurrency", func(c *Config) { c.Agent.MaxConcurrent = 0 }, true},
		{"no enabled source", func(c *Config) { c.Sources[0].Enabled = false }, true},
		{"duplicate source name", func(c *Config) {
			c.Sources = append(c.Sources, SourceConfig{Name: "s1", Type: "jsearch", Enabled: true})
		}, true},
		{"source missing type", func(c *Config) { c.Sources[0].Type = "" }, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := baseValid()
			tt.mutate(c)
			err := c.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoadExpandsEnvAndDefaults(t *testing.T) {
	t.Setenv("TEST_DSN", "postgres://localhost/jobagent")
	t.Setenv("TEST_KEY", "sk-secret")

	yaml := `
agent:
  dry_run: true
profile:
  full_name: "Jane Dev"
  email: "jane@example.com"
sources:
  - name: s1
    type: jsearch
    enabled: true
matching:
  api_key: "${TEST_KEY}"
database:
  dsn: "${TEST_DSN}"
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}

	if cfg.Database.DSN != "postgres://localhost/jobagent" {
		t.Errorf("DSN not expanded: %q", cfg.Database.DSN)
	}
	if cfg.Matching.APIKey != "sk-secret" {
		t.Errorf("api key not expanded: %q", cfg.Matching.APIKey)
	}
	// Defaults applied.
	if cfg.Agent.Interval != 4*time.Hour {
		t.Errorf("default interval = %v, want 4h", cfg.Agent.Interval)
	}
	if cfg.Matching.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("default model = %q", cfg.Matching.Model)
	}
	if cfg.Matching.MinScore != 0.6 {
		t.Errorf("default min_score = %v", cfg.Matching.MinScore)
	}
}

func TestEnabledSources(t *testing.T) {
	c := &Config{Sources: []SourceConfig{
		{Name: "a", Enabled: true},
		{Name: "b", Enabled: false},
		{Name: "c", Enabled: true},
	}}
	got := c.EnabledSources()
	if len(got) != 2 {
		t.Fatalf("EnabledSources() = %d, want 2", len(got))
	}
}
