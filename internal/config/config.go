// Package config loads daemon configuration with a predictable precedence:
// built-in defaults, then an optional JSON file, then NOEMA_* environment
// variables. Runtime tunables that live in the database are handled by the
// settings store, not here.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the fully resolved daemon configuration.
type Config struct {
	HTTPAddr  string `json:"http_addr"`
	Env       string `json:"env"` // development | production
	LogLevel  string `json:"log_level"`
	LogFormat string `json:"log_format"` // text | json

	DatabaseURL string `json:"database_url"`
	MasterKey   string `json:"master_key"` // base64, 32 bytes; secret

	BootstrapAdminUser     string `json:"bootstrap_admin_user"`
	BootstrapAdminPassword string `json:"bootstrap_admin_password"` // secret

	ResearchDir string `json:"research_dir"`

	MaxWorkers       int   `json:"max_workers"`
	MaxConcurrentLLM int   `json:"max_concurrent_llm"`
	DailyTokenBudget int64 `json:"daily_token_budget"`

	SessionIdleTTL     time.Duration `json:"session_idle_ttl"`
	SessionAbsoluteTTL time.Duration `json:"session_absolute_ttl"`
	MaxRequestBody     int64         `json:"max_request_body"`
}

// Defaults returns the built-in configuration.
func Defaults() Config {
	return Config{
		HTTPAddr:           "127.0.0.1:8080",
		Env:                "development",
		LogLevel:           "info",
		LogFormat:          "text",
		BootstrapAdminUser: "admin",
		ResearchDir:        "./research",
		MaxWorkers:         8,
		MaxConcurrentLLM:   4,
		DailyTokenBudget:   2_000_000,
		SessionIdleTTL:     12 * time.Hour,
		SessionAbsoluteTTL: 7 * 24 * time.Hour,
		MaxRequestBody:     1 << 20,
	}
}

// Load resolves configuration from defaults, the optional file at path, and
// the environment provided by lookup (normally os.LookupEnv).
func Load(path string, lookup func(string) (string, bool)) (Config, error) {
	c := Defaults()
	if path != "" {
		b, err := os.ReadFile(path) // #nosec G304 -- operator-supplied config path from the command line
		if err != nil {
			return c, fmt.Errorf("config: read %s: %w", path, err)
		}
		dec := json.NewDecoder(strings.NewReader(string(b)))
		dec.DisallowUnknownFields()
		var f fileConfig
		if err := dec.Decode(&f); err != nil {
			return c, fmt.Errorf("config: parse %s: %w", path, err)
		}
		f.apply(&c)
	}
	if err := c.applyEnv(lookup); err != nil {
		return c, err
	}
	return c, c.Validate()
}

// fileConfig mirrors Config but with durations as strings so JSON stays readable.
type fileConfig struct {
	HTTPAddr               *string `json:"http_addr"`
	Env                    *string `json:"env"`
	LogLevel               *string `json:"log_level"`
	LogFormat              *string `json:"log_format"`
	DatabaseURL            *string `json:"database_url"`
	MasterKey              *string `json:"master_key"`
	BootstrapAdminUser     *string `json:"bootstrap_admin_user"`
	BootstrapAdminPassword *string `json:"bootstrap_admin_password"`
	ResearchDir            *string `json:"research_dir"`
	MaxWorkers             *int    `json:"max_workers"`
	MaxConcurrentLLM       *int    `json:"max_concurrent_llm"`
	DailyTokenBudget       *int64  `json:"daily_token_budget"`
	SessionIdleTTL         *string `json:"session_idle_ttl"`
	SessionAbsoluteTTL     *string `json:"session_absolute_ttl"`
	MaxRequestBody         *int64  `json:"max_request_body"`
}

func (f fileConfig) apply(c *Config) {
	setS := func(dst *string, src *string) {
		if src != nil {
			*dst = *src
		}
	}
	setS(&c.HTTPAddr, f.HTTPAddr)
	setS(&c.Env, f.Env)
	setS(&c.LogLevel, f.LogLevel)
	setS(&c.LogFormat, f.LogFormat)
	setS(&c.DatabaseURL, f.DatabaseURL)
	setS(&c.MasterKey, f.MasterKey)
	setS(&c.BootstrapAdminUser, f.BootstrapAdminUser)
	setS(&c.BootstrapAdminPassword, f.BootstrapAdminPassword)
	setS(&c.ResearchDir, f.ResearchDir)
	if f.MaxWorkers != nil {
		c.MaxWorkers = *f.MaxWorkers
	}
	if f.MaxConcurrentLLM != nil {
		c.MaxConcurrentLLM = *f.MaxConcurrentLLM
	}
	if f.DailyTokenBudget != nil {
		c.DailyTokenBudget = *f.DailyTokenBudget
	}
	if f.MaxRequestBody != nil {
		c.MaxRequestBody = *f.MaxRequestBody
	}
	if f.SessionIdleTTL != nil {
		if d, err := time.ParseDuration(*f.SessionIdleTTL); err == nil {
			c.SessionIdleTTL = d
		}
	}
	if f.SessionAbsoluteTTL != nil {
		if d, err := time.ParseDuration(*f.SessionAbsoluteTTL); err == nil {
			c.SessionAbsoluteTTL = d
		}
	}
}

func (c *Config) applyEnv(lookup func(string) (string, bool)) error {
	str := func(key string, dst *string) {
		if v, ok := lookup("NOEMA_" + key); ok {
			*dst = v
		}
	}
	var errs []error
	integer := func(key string, dst *int) {
		if v, ok := lookup("NOEMA_" + key); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				errs = append(errs, fmt.Errorf("NOEMA_%s: %w", key, err))
				return
			}
			*dst = n
		}
	}
	integer64 := func(key string, dst *int64) {
		if v, ok := lookup("NOEMA_" + key); ok {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				errs = append(errs, fmt.Errorf("NOEMA_%s: %w", key, err))
				return
			}
			*dst = n
		}
	}
	dur := func(key string, dst *time.Duration) {
		if v, ok := lookup("NOEMA_" + key); ok {
			d, err := time.ParseDuration(v)
			if err != nil {
				errs = append(errs, fmt.Errorf("NOEMA_%s: %w", key, err))
				return
			}
			*dst = d
		}
	}
	str("HTTP_ADDR", &c.HTTPAddr)
	str("ENV", &c.Env)
	str("LOG_LEVEL", &c.LogLevel)
	str("LOG_FORMAT", &c.LogFormat)
	str("DATABASE_URL", &c.DatabaseURL)
	str("MASTER_KEY", &c.MasterKey)
	str("BOOTSTRAP_ADMIN_USER", &c.BootstrapAdminUser)
	str("BOOTSTRAP_ADMIN_PASSWORD", &c.BootstrapAdminPassword)
	str("RESEARCH_DIR", &c.ResearchDir)
	integer("MAX_WORKERS", &c.MaxWorkers)
	integer("MAX_CONCURRENT_LLM", &c.MaxConcurrentLLM)
	integer64("DAILY_TOKEN_BUDGET", &c.DailyTokenBudget)
	integer64("MAX_REQUEST_BODY", &c.MaxRequestBody)
	dur("SESSION_IDLE_TTL", &c.SessionIdleTTL)
	dur("SESSION_ABSOLUTE_TTL", &c.SessionAbsoluteTTL)
	return errors.Join(errs...)
}

// Validate checks invariants that would otherwise fail later and less clearly.
func (c Config) Validate() error {
	var errs []error
	if c.Env != "development" && c.Env != "production" {
		errs = append(errs, fmt.Errorf("env must be development or production, got %q", c.Env))
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("log_level %q invalid", c.LogLevel))
	}
	if c.LogFormat != "text" && c.LogFormat != "json" {
		errs = append(errs, fmt.Errorf("log_format %q invalid", c.LogFormat))
	}
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("database_url is required"))
	} else if _, err := url.Parse(c.DatabaseURL); err != nil {
		errs = append(errs, fmt.Errorf("database_url: %w", err))
	}
	if c.MaxWorkers < 1 || c.MaxConcurrentLLM < 1 {
		errs = append(errs, errors.New("max_workers and max_concurrent_llm must be >= 1"))
	}
	if c.SessionIdleTTL <= 0 || c.SessionAbsoluteTTL < c.SessionIdleTTL {
		errs = append(errs, errors.New("session ttls must be positive and absolute >= idle"))
	}
	if c.MaxRequestBody < 4096 {
		errs = append(errs, errors.New("max_request_body too small"))
	}
	return errors.Join(errs...)
}

// Production reports whether secure-by-default production behaviour applies.
func (c Config) Production() bool { return c.Env == "production" }

// Redacted returns a copy safe for logging: secrets and DSN passwords removed.
func (c Config) Redacted() Config {
	r := c
	if r.MasterKey != "" {
		r.MasterKey = "[set]"
	}
	if r.BootstrapAdminPassword != "" {
		r.BootstrapAdminPassword = "[set]"
	}
	if u, err := url.Parse(r.DatabaseURL); err == nil && u.User != nil {
		if _, has := u.User.Password(); has {
			u.User = url.UserPassword(u.User.Username(), "xxxxx")
			r.DatabaseURL = u.String()
		}
	}
	return r
}

// Digest is a stable hash of the non-secret configuration: it changes when
// the daemon would behave differently and stays the same when only secrets
// or credentials rotate. Used to explain replay differences.
func (c Config) Digest() string {
	public := struct {
		HTTPAddr, Env, LogLevel, LogFormat, ResearchDir string
		MaxWorkers, MaxConcurrentLLM                    int
		DailyTokenBudget, MaxRequestBody                int64
		SessionIdleTTL, SessionAbsoluteTTL              time.Duration
		HasMasterKey                                    bool
	}{c.HTTPAddr, c.Env, c.LogLevel, c.LogFormat, c.ResearchDir, c.MaxWorkers, c.MaxConcurrentLLM, c.DailyTokenBudget, c.MaxRequestBody, c.SessionIdleTTL, c.SessionAbsoluteTTL, c.MasterKey != ""}
	b, _ := json.Marshal(public)
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:8])
}
