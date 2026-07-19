package doctor

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	MaxReportChecks            = 256
	MaxSelectedChecks          = 64
	MaxObservationsPerCheck    = 16
	MaxObservationSummaryBytes = 2 * 1024
	MaxReportBytes             = 2 * 1024 * 1024
)

type Mode string

const (
	ModeBootstrap Mode = "bootstrap"
	ModeDiagnose  Mode = "diagnose"
)

func (m Mode) Validate() error {
	switch m {
	case ModeBootstrap, ModeDiagnose:
		return nil
	default:
		return fmt.Errorf("invalid doctor mode %q", m)
	}
}

type CheckStatus string

const (
	StatusPass    CheckStatus = "PASS"
	StatusWarn    CheckStatus = "WARN"
	StatusFail    CheckStatus = "FAIL"
	StatusUnknown CheckStatus = "UNKNOWN"
	StatusBlocked CheckStatus = "BLOCKED"
	StatusSkipped CheckStatus = "SKIPPED"
)

func (s CheckStatus) Validate() error {
	switch s {
	case StatusPass, StatusWarn, StatusFail, StatusUnknown, StatusBlocked, StatusSkipped:
		return nil
	default:
		return fmt.Errorf("invalid check status %q", s)
	}
}

type Conclusion string

const (
	ConclusionHealthy      Conclusion = "HEALTHY"
	ConclusionDegraded     Conclusion = "DEGRADED"
	ConclusionUnhealthy    Conclusion = "UNHEALTHY"
	ConclusionInconclusive Conclusion = "INCONCLUSIVE"
)

func (c Conclusion) Validate() error {
	switch c {
	case ConclusionHealthy, ConclusionDegraded, ConclusionUnhealthy, ConclusionInconclusive:
		return nil
	default:
		return fmt.Errorf("invalid doctor conclusion %q", c)
	}
}

type Observation struct {
	Source     string    `json:"source" yaml:"source"`
	ObservedAt time.Time `json:"observed_at,omitempty" yaml:"observed_at,omitempty"`
	ExpiresAt  time.Time `json:"expires_at,omitempty" yaml:"expires_at,omitempty"`
	Summary    string    `json:"summary" yaml:"summary"`
	Digest     string    `json:"digest,omitempty" yaml:"digest,omitempty"`
	Error      string    `json:"error,omitempty" yaml:"error,omitempty"`
}

func (o Observation) Validate() error {
	if strings.TrimSpace(o.Source) == "" {
		return errors.New("observation source is required")
	}
	if len(o.Summary) > MaxObservationSummaryBytes {
		return fmt.Errorf("observation summary is %d bytes, limit is %d", len(o.Summary), MaxObservationSummaryBytes)
	}
	if !o.ExpiresAt.IsZero() && !o.ObservedAt.IsZero() && o.ExpiresAt.Before(o.ObservedAt) {
		return errors.New("observation expires_at precedes observed_at")
	}
	if o.Digest != "" {
		const prefix = "sha256:"
		if !strings.HasPrefix(o.Digest, prefix) || len(o.Digest) != len(prefix)+sha256HexLength {
			return errors.New("observation digest must be sha256 followed by 64 hexadecimal characters")
		}
		if _, err := hex.DecodeString(strings.TrimPrefix(o.Digest, prefix)); err != nil {
			return errors.New("observation digest must be sha256 followed by 64 hexadecimal characters")
		}
	}
	return nil
}

const sha256HexLength = 64

type DependencyContext struct {
	CheckID  string      `json:"check_id"`
	Status   CheckStatus `json:"status"`
	Summary  string      `json:"summary,omitempty"`
	Required bool        `json:"required"`
}

type CheckSpec struct {
	ID                   string
	RequiredDependencies []string
	OptionalDependencies []string
	Timeout              time.Duration
}

type CheckResult struct {
	ID                string              `json:"id"`
	Status            CheckStatus         `json:"status"`
	Summary           string              `json:"summary,omitempty"`
	Error             string              `json:"error,omitempty"`
	StartedAt         time.Time           `json:"started_at,omitempty"`
	FinishedAt        time.Time           `json:"finished_at,omitempty"`
	Observations      []Observation       `json:"observations,omitempty"`
	DependencyContext []DependencyContext `json:"dependency_context,omitempty"`
	RecoveryActionIDs []string            `json:"recovery_action_ids,omitempty"`
}

func (r CheckResult) Validate() error {
	if strings.TrimSpace(r.ID) == "" {
		return errors.New("check id is required")
	}
	if err := r.Status.Validate(); err != nil {
		return err
	}
	if len(r.Observations) > MaxObservationsPerCheck {
		return fmt.Errorf("check %q has %d observations, limit is %d", r.ID, len(r.Observations), MaxObservationsPerCheck)
	}
	for i, observation := range r.Observations {
		if err := observation.Validate(); err != nil {
			return fmt.Errorf("check %q observation %d: %w", r.ID, i, err)
		}
	}
	return nil
}
