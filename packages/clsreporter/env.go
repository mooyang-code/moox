package clsreporter

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	defaultTimeout = 800 * time.Millisecond
	minTimeout     = 100 * time.Millisecond
	maxTimeout     = time.Second
)

// Config contains all connection data needed to submit a CLS batch. It is
// deliberately explicit so callers do not depend on tRPC or YAML globals.
type Config struct {
	Endpoint  string
	TopicID   string
	SecretID  string
	SecretKey string
	Source    string
	Timeout   time.Duration
}

// ConfigFromEnv reads only the SCF environment contract. A disabled reporter
// has no required connection settings and becomes a Noop reporter.
func ConfigFromEnv(getenv func(string) string) (Config, bool, error) {
	if getenv == nil {
		return Config{}, false, fmt.Errorf("environment lookup is required")
	}
	if !strings.EqualFold(strings.TrimSpace(getenv("MOOX_CLS_ENABLED")), "true") {
		return Config{}, false, nil
	}
	cfg := Config{
		Endpoint:  strings.TrimSpace(getenv("MOOX_CLS_ENDPOINT")),
		TopicID:   strings.TrimSpace(getenv("MOOX_CLS_TOPIC_ID")),
		SecretID:  strings.TrimSpace(getenv("MOOX_CLS_SECRET_ID")),
		SecretKey: strings.TrimSpace(getenv("MOOX_CLS_SECRET_KEY")),
		Source:    strings.TrimSpace(getenv("MOOX_CLS_SOURCE")),
		Timeout:   defaultTimeout,
	}
	for key, value := range map[string]string{
		"MOOX_CLS_ENDPOINT": cfg.Endpoint, "MOOX_CLS_TOPIC_ID": cfg.TopicID,
		"MOOX_CLS_SECRET_ID": cfg.SecretID, "MOOX_CLS_SECRET_KEY": cfg.SecretKey,
	} {
		if value == "" {
			return Config{}, false, fmt.Errorf("%s is required when CLS reporting is enabled", key)
		}
	}
	if raw := strings.TrimSpace(getenv("MOOX_CLS_TIMEOUT_MS")); raw != "" {
		milliseconds, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, false, fmt.Errorf("MOOX_CLS_TIMEOUT_MS must be an integer: %w", err)
		}
		cfg.Timeout = time.Duration(milliseconds) * time.Millisecond
	}
	if cfg.Timeout < minTimeout || cfg.Timeout > maxTimeout {
		return Config{}, false, fmt.Errorf("MOOX_CLS_TIMEOUT_MS must be between %d and %d", minTimeout/time.Millisecond, maxTimeout/time.Millisecond)
	}
	return cfg, true, nil
}
