package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
)

func TestOutputManifestDeleteBefore(t *testing.T) {
	db, err := Open(&Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.ApplySchema(factorschema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	missing, err := db.OutputManifests().Get(ctx, OutputManifestKey{
		BindingID: "missing", SubjectID: "BTC-USDT", Frequency: "1m", PeriodTime: time.Now().UTC(),
	})
	if err != nil || missing != nil {
		t.Fatalf("missing manifest = %v, err=%v", missing, err)
	}
	if err := db.db.Exec(`
		INSERT INTO t_factor_defs (
			c_factor_id, c_name, c_source_code, c_source_hash,
			c_input_columns_json, c_outputs_json, c_lookback_periods, c_status
		) VALUES ('factor', 'factor', 'def compute(df, params): return {}', 'hash', '[]', '[]', 1, 'disabled');
		INSERT INTO t_factor_bindings (
			c_binding_id, c_factor_id, c_space_id, c_source_view_id, c_freq,
			c_subject_mode, c_subjects_json, c_result_dataset_id, c_result_view_id, c_status
		) VALUES ('binding', 'factor', 'space', 'source-view', '1m', 'all', '[]', 'result-dataset', 'result-view', 'disabled');
	`).Error; err != nil {
		t.Fatal(err)
	}
	oldPeriod := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	newPeriod := time.Date(2026, time.January, 8, 0, 0, 0, 0, time.UTC)
	for _, period := range []time.Time{oldPeriod, newPeriod} {
		if err := db.OutputManifests().Replace(ctx, OutputManifestKey{BindingID: "binding", SubjectID: "BTC-USDT", Frequency: "1m", PeriodTime: period}, []string{"row-" + period.Format("20060102")}); err != nil {
			t.Fatal(err)
		}
	}
	deleted, err := db.OutputManifests().DeleteBefore(ctx, time.Date(2026, time.January, 5, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted manifests = %d, want 1", deleted)
	}
	keys, err := db.OutputManifests().ListByBinding(ctx, "binding")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || !keys[0].PeriodTime.Equal(newPeriod) {
		t.Fatalf("remaining manifests = %+v", keys)
	}
}
