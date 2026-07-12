package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/bootstrap"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun_InitAndImport(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "factor.db")
	factorsDir := filepath.Join(tmp, "factors")
	require.NoError(t, os.MkdirAll(factorsDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(factorsDir, "Bias.py"), []byte("def signal(df, n):\n    return df['close']\n"), 0o644))

	var out bytes.Buffer
	require.NoError(t, run(context.Background(), []string{"init", "--db", dbPath}, &out))
	assert.Contains(t, out.String(), `"ok":true`)

	out.Reset()
	require.NoError(t, run(context.Background(), []string{"import", "--db", dbPath, "--factors-dir", factorsDir, "--default-params", "20"}, &out))
	assert.Contains(t, out.String(), `"ok":true`)
	assert.Contains(t, out.String(), "bias")
}

func TestRun_UnknownCommandAndMissingArgs(t *testing.T) {
	err := run(context.Background(), nil, &bytes.Buffer{})
	require.Error(t, err)

	err = run(context.Background(), []string{"nope"}, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown command")
}

func TestRunOnce_ValidationAndEmptyFactors(t *testing.T) {
	err := runOnce(context.Background(), cliConfig{}, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required")

	dbPath := filepath.Join(t.TempDir(), "factor.db")
	err = runOnce(context.Background(), cliConfig{
		DBPath:    dbPath,
		SpaceID:   "crypto",
		DatasetID: "kline",
		SubjectID: "BTC",
		Freq:      "1m",
		BarTime:   time.Unix(1, 0).UTC(),
	}, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no enabled factors")
}

func TestRunOnce_SyncFailsAgainstUnreachableMetadata(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "factor.db")
	db, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))
	require.NoError(t, db.Factors().Upsert(context.Background(), domain.FactorDef{
		FactorID:     "bias",
		Name:         "Bias",
		Kind:         domain.FactorKindTimeseries,
		SourceCode:   "x",
		SourceHash:   "h",
		ParamsJSON:   `[20]`,
		LookbackBars: 20,
		Status:       domain.FactorStatusEnabled,
	}))
	require.NoError(t, db.Close())

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err = runOnce(ctx, cliConfig{
		DBPath:     dbPath,
		FactorsDir: tmp,
		SpaceID:    "crypto",
		DatasetID:  "kline",
		SubjectID:  "BTC",
		Freq:       "1m",
		BarTime:    time.Unix(1, 0).UTC(),
		FactorIDs:  []string{"bias"},
	}, &bytes.Buffer{})
	require.Error(t, err)
}

func TestMetadataAdapter_DelegatesToTRPCProxy(t *testing.T) {
	adapter := metadataAdapter{proxy: storagepb.NewMetadataClientProxy()}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, _ = adapter.CreateFactor(ctx, &storagepb.CreateFactorReq{})
	_, _ = adapter.CreateDataset(ctx, &storagepb.CreateDatasetReq{})
	_, _ = adapter.UpdateDataset(ctx, &storagepb.UpdateDatasetReq{})
	_, _ = adapter.UpsertDatasetColumn(ctx, &storagepb.UpsertDatasetColumnReq{})
	_, _ = adapter.GetFactor(ctx, &storagepb.GetFactorReq{})
	_, _ = adapter.GetDataset(ctx, &storagepb.GetDatasetReq{})
	_, _ = adapter.ListDatasetColumns(ctx, &storagepb.ListDatasetColumnsReq{})
	_, _ = adapter.ListDatasetSubjects(ctx, &storagepb.ListDatasetSubjectsReq{})
	_, _ = adapter.BindDatasetSubject(ctx, &storagepb.BindDatasetSubjectReq{})
	_, _ = adapter.ListPrimaryStoreRoutes(ctx, &storagepb.ListPrimaryStoreRoutesReq{})
	_, _ = adapter.CreatePrimaryStoreRoute(ctx, &storagepb.CreatePrimaryStoreRouteReq{})
}

func TestServiceAuth_AndLogRunOnce(t *testing.T) {
	cfg := bootstrap.Default()
	cfg.SysDeploy.ServiceAuth.AccessKey = "ak"
	cfg.SysDeploy.ServiceAuth.SecretKey = "sk"
	auth := serviceAuth(cfg)
	require.NotNil(t, auth)
	assert.Equal(t, "ak", auth.AppId)
	assert.Equal(t, "sk", auth.AppKey)

	task := &engine.FactorTask{TaskID: "t1", SpaceID: "s", SourceDataset: "d", SubjectID: "BTC", Freq: "1m", BarTime: time.Unix(0, 0).UTC()}
	logRunOnce(context.Background(), task, domain.RunStatusFailed, "boom", 3)
	payload := runOncePayload(task, domain.RunStatusFailed, 1, 3)
	assert.Equal(t, false, payload["ok"])
	assert.Equal(t, "t1-failed", payload["run_id"])
}

func TestParseArgs_ErrorPaths(t *testing.T) {
	_, err := parseArgs([]string{"import", "--default-params", "x"})
	require.Error(t, err)

	_, err = parseArgs([]string{"run-once", "--bar-time", "bad"})
	require.Error(t, err)

	cfg, err := parseArgs([]string{"run-once", "--factors", " a , b "})
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, cfg.FactorIDs)
}
