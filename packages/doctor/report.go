package doctor

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"time"
)

const ReportSchemaVersion = "doctor.moox.dev/v1"

//go:embed report.schema.json
var embeddedReportSchema []byte

type Report struct {
	SchemaVersion       string        `json:"schema_version"`
	RunID               string        `json:"run_id"`
	Mode                Mode          `json:"mode"`
	StartedAt           time.Time     `json:"started_at"`
	FinishedAt          time.Time     `json:"finished_at"`
	Conclusion          Conclusion    `json:"conclusion"`
	Checks              []CheckResult `json:"checks"`
	RootCauseCheckIDs   []string      `json:"root_cause_check_ids"`
	BlockingCheckIDs    []string      `json:"blocking_check_ids"`
	MissingObservations []Observation `json:"missing_observations"`
	ManifestChecksum    string        `json:"manifest_checksum"`

	executionErr error
}

func (r Report) CheckByID(id string) *CheckResult {
	for i := range r.Checks {
		if r.Checks[i].ID == id {
			return &r.Checks[i]
		}
	}
	return nil
}

func (r Report) ExecutionError() error {
	return r.executionErr
}

func (r Report) Validate() error {
	if r.SchemaVersion != ReportSchemaVersion {
		return fmt.Errorf("unsupported report schema version %q", r.SchemaVersion)
	}
	if r.RunID == "" {
		return fmt.Errorf("report run_id is required")
	}
	if err := r.Mode.Validate(); err != nil {
		return err
	}
	if err := r.Conclusion.Validate(); err != nil {
		return err
	}
	if len(r.Checks) > MaxReportChecks {
		return fmt.Errorf("report has %d checks, limit is %d", len(r.Checks), MaxReportChecks)
	}
	seen := make(map[string]bool, len(r.Checks))
	for _, check := range r.Checks {
		if err := check.Validate(); err != nil {
			return err
		}
		if seen[check.ID] {
			return fmt.Errorf("duplicate report check %q", check.ID)
		}
		seen[check.ID] = true
	}
	for i, observation := range r.MissingObservations {
		if err := observation.Validate(); err != nil {
			return fmt.Errorf("missing observation %d: %w", i, err)
		}
	}
	return nil
}

func (r Report) MarshalJSONBounded() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	if r.Checks == nil {
		r.Checks = []CheckResult{}
	}
	if r.RootCauseCheckIDs == nil {
		r.RootCauseCheckIDs = []string{}
	}
	if r.BlockingCheckIDs == nil {
		r.BlockingCheckIDs = []string{}
	}
	if r.MissingObservations == nil {
		r.MissingObservations = []Observation{}
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	if len(data) > MaxReportBytes {
		return nil, fmt.Errorf("report is %d bytes, limit is %d", len(data), MaxReportBytes)
	}
	return append(data, '\n'), nil
}
