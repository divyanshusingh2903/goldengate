// Package config loads and saves the user's TOML configuration and resolves
// XDG-style config and state directories. Files are written 0600 in 0700
// directories. No secrets live here: tokens belong in the OS keyring.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/OWNER/goldengate/internal/model"
)

// Config mirrors DESIGN.md section 5.
type Config struct {
	Server    Server    `toml:"server"`
	Share     Share     `toml:"share"`
	Artifacts Artifacts `toml:"artifacts"`
	Enrich    Enrich    `toml:"enrich"`
	Issues    Issues    `toml:"issues"`
	Redact    Redact    `toml:"redact"`
}

// Server is the bridge endpoint. The token is never stored here.
type Server struct {
	URL string `toml:"url"`
}

// Share is the selection policy.
type Share struct {
	Default        string   `toml:"default"`
	Allow          []string `toml:"allow"`
	Deny           []string `toml:"deny"`
	IncludeSources []string `toml:"include_sources"`
	Since          string   `toml:"since"`
	Level          string   `toml:"level"`
}

// Artifacts controls whether contents (not just metadata) may be sent.
type Artifacts struct {
	IncludeContent bool `toml:"include_content"`
}

// Enrich holds the opt-in network/local enrichments. All default to off.
type Enrich struct {
	Git    bool `toml:"git"`
	GitHub bool `toml:"github"`
}

// Issues configures tracker extraction.
type Issues struct {
	Providers    []string `toml:"providers"`
	LinearTeams  []string `toml:"linear_teams"`
	JiraProjects []string `toml:"jira_projects"`
	RefreshState bool     `toml:"refresh_state"`
}

// Redact holds user redaction settings.
type Redact struct {
	ExtraPatterns   []string `toml:"extra_patterns"`
	StripHomePrefix bool     `toml:"strip_home_prefix"`
}

// Default returns the default-deny configuration.
func Default() Config {
	return Config{
		Share: Share{
			Default:        "deny",
			IncludeSources: []string{"claude-code", "codex", "opencode", "claude-ai", "chatgpt"},
			Since:          "90d",
			Level:          model.LevelSummary,
		},
		Issues: Issues{Providers: []string{"linear", "jira", "github"}},
		Redact: Redact{StripHomePrefix: true},
	}
}

// Validate checks the configuration for unsafe or inconsistent values.
func (c Config) Validate() error {
	switch c.Share.Default {
	case "deny", "allow":
	default:
		return fmt.Errorf("config: share.default must be deny or allow, got %q", c.Share.Default)
	}
	switch c.Share.Level {
	case model.LevelTitlesOnly, model.LevelSummary:
	case model.LevelFull:
		return errors.New("config: share.level = \"full\" is not supported in this build")
	default:
		return fmt.Errorf("config: share.level must be titles-only or summary, got %q", c.Share.Level)
	}
	if c.Artifacts.IncludeContent && c.Share.Level != model.LevelFull {
		return errors.New("config: artifacts.include_content requires share.level = \"full\"")
	}
	if _, err := ParseSince(c.Share.Since); err != nil {
		return err
	}
	if c.Server.URL != "" {
		if err := CheckServerURL(c.Server.URL); err != nil {
			return err
		}
	}
	return nil
}

// CheckServerURL refuses plain http except for loopback hosts.
func CheckServerURL(raw string) error {
	lower := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(lower, "https://"):
		return nil
	case strings.HasPrefix(lower, "http://"):
		host := strings.TrimPrefix(lower, "http://")
		host = strings.SplitN(host, "/", 2)[0]
		if h, _, ok := strings.Cut(host, ":"); ok {
			host = h
		}
		if host == "localhost" || host == "127.0.0.1" || host == "[::1]" || host == "::1" {
			return nil
		}
		return fmt.Errorf("config: server.url must use https (plain http is only allowed for loopback): %q", raw)
	}
	return fmt.Errorf("config: server.url must start with https://: %q", raw)
}

// ParseSince parses windows like "90d", "12h", "2w". Empty means no limit.
func ParseSince(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	unit := s[len(s)-1]
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n < 0 {
		return 0, fmt.Errorf("config: invalid since window %q (examples: 7d, 12h, 2w)", s)
	}
	switch unit {
	case 'h':
		return time.Duration(n) * time.Hour, nil
	case 'd':
		return time.Duration(n) * 24 * time.Hour, nil
	case 'w':
		return time.Duration(n) * 7 * 24 * time.Hour, nil
	}
	return 0, fmt.Errorf("config: invalid since window %q (examples: 7d, 12h, 2w)", s)
}

// Dirs resolves where config and state live.
type Dirs struct{ Config, State string }

// ResolveDirs follows XDG on Unix and %AppData%/%LocalAppData% on Windows.
// env and home are injectable for tests.
func ResolveDirs(env func(string) string, home string) Dirs {
	if runtime.GOOS == "windows" {
		cfg := env("APPDATA")
		if cfg == "" {
			cfg = filepath.Join(home, "AppData", "Roaming")
		}
		st := env("LOCALAPPDATA")
		if st == "" {
			st = filepath.Join(home, "AppData", "Local")
		}
		return Dirs{Config: filepath.Join(cfg, "goldengate"), State: filepath.Join(st, "goldengate")}
	}
	cfg := env("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	st := env("XDG_STATE_HOME")
	if st == "" {
		st = filepath.Join(home, ".local", "state")
	}
	return Dirs{Config: filepath.Join(cfg, "goldengate"), State: filepath.Join(st, "goldengate")}
}

// ConfigPath is the TOML file path.
func (d Dirs) ConfigPath() string { return filepath.Join(d.Config, "config.toml") }

// StatePath is the sync state database path.
func (d Dirs) StatePath() string { return filepath.Join(d.State, "state.db") }

// Load reads the config at path. A missing file yields Default().
func Load(path string) (Config, error) {
	c := Default()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, fmt.Errorf("config: read %s: %w", path, err)
	}
	dec := toml.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Default(), fmt.Errorf("config: parse %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return Default(), err
	}
	return c, nil
}

// Save writes the config atomically with mode 0600. Comments in an existing
// file are not preserved.
func Save(path string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("config: mkdir: %w", err)
	}
	b, err := toml.Marshal(c)
	if err != nil {
		return fmt.Errorf("config: encode: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("config: temp file: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		_ = tmp.Close()
		return fmt.Errorf("config: chmod: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("config: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: close: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("config: replace: %w", err)
	}
	return nil
}
