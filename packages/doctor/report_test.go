package doctor

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
)

func TestReportJSONMatchesSchemaAndIsStable(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	report := Report{
		SchemaVersion:    "doctor.moox.dev/v1",
		RunID:            "run-1",
		Mode:             ModeDiagnose,
		StartedAt:        now,
		FinishedAt:       now.Add(time.Second),
		Conclusion:       ConclusionHealthy,
		Checks:           []CheckResult{{ID: "service.health:monitor@local", Status: StatusPass, Summary: "ready"}},
		ManifestChecksum: "sha256:manifest",
	}
	one, err := report.MarshalJSONBounded()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	two, err := report.MarshalJSONBounded()
	if err != nil {
		t.Fatalf("marshal again: %v", err)
	}
	if !bytes.Equal(one, two) {
		t.Fatalf("serialization is unstable:\n%s\n%s", one, two)
	}
	want, err := os.ReadFile("testdata/report.golden.json")
	if err != nil {
		t.Fatalf("read golden report: %v", err)
	}
	if !bytes.Equal(one, want) {
		t.Fatalf("report changed from golden:\n%s", one)
	}

	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("report.schema.json", bytes.NewReader(embeddedReportSchema)); err != nil {
		t.Fatalf("add schema: %v", err)
	}
	schema, err := compiler.Compile("report.schema.json")
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	var document any
	if err := json.Unmarshal(one, &document); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if err := schema.Validate(document); err != nil {
		t.Fatalf("schema validation: %v\n%s", err, one)
	}
}

func TestReportLimitsFailClosed(t *testing.T) {
	t.Parallel()

	report := Report{SchemaVersion: "doctor.moox.dev/v1", RunID: "run", Mode: ModeDiagnose, Conclusion: ConclusionHealthy}
	for i := 0; i <= MaxReportChecks; i++ {
		report.Checks = append(report.Checks, CheckResult{ID: "check-" + strings.Repeat("x", i%5), Status: StatusPass})
	}
	if _, err := report.MarshalJSONBounded(); err == nil {
		t.Fatal("oversized check count accepted")
	}
}

func TestReportRequiresRunID(t *testing.T) {
	t.Parallel()

	report := Report{SchemaVersion: ReportSchemaVersion, Mode: ModeDiagnose, Conclusion: ConclusionHealthy}
	if _, err := report.MarshalJSONBounded(); err == nil {
		t.Fatal("report without run_id accepted")
	}
}

func TestCheckObservationLimit(t *testing.T) {
	t.Parallel()

	check := CheckResult{ID: "check", Status: StatusPass}
	for i := 0; i <= MaxObservationsPerCheck; i++ {
		check.Observations = append(check.Observations, Observation{Source: "source", Summary: "ok"})
	}
	if err := check.Validate(); err == nil {
		t.Fatal("oversized observations accepted")
	}
}
