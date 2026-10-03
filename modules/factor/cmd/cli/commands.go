package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/catalog"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/pyexec"
	"github.com/mooyang-code/moox/modules/factor/internal/recalc"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorgen "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/modules/factor/schema"
	storagegen "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"google.golang.org/protobuf/encoding/protojson"
	"trpc.group/trpc-go/trpc-go/client"
)

type catalogEntry struct {
	FactorType      string         `json:"factor_type"`
	File            string         `json:"file"`
	FactorID        string         `json:"factor_id"`
	InputColumns    []string       `json:"input_columns"`
	Outputs         []string       `json:"outputs"`
	Params          map[string]any `json:"params"`
	LookbackPeriods int            `json:"lookback_periods"`
}

func runInit(cfg cliConfig, out io.Writer) error {
	db, err := store.Open(&store.Options{Path: cfg.DBPath})
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.ApplySchema(schema.AllSQL()); err != nil {
		return fmt.Errorf("apply factor schema: %w", err)
	}
	return json.NewEncoder(out).Encode(map[string]any{
		"ok": true, "database": cfg.DBPath,
		"tables": []string{"t_factor_sets", "t_factor_defs", "t_factor_recalc_jobs"},
	})
}

func runImport(ctx context.Context, cfg cliConfig, out io.Writer) error {
	if cfg.SetID == "" || cfg.File == "" || cfg.FactorID == "" || len(cfg.InputColumns) == 0 || len(cfg.Outputs) == 0 || cfg.LookbackPeriods < 1 {
		return errors.New("--set, --file, --factor-id, --inputs, --outputs and --lookback are required")
	}
	runtime, err := loadRuntimeConfig(cfg.ConfigPath)
	if err != nil {
		return err
	}
	if cfg.DBPath != "" {
		runtime.DatabasePath = cfg.DBPath
	}
	if cfg.FactorsDir != "" {
		runtime.FactorsDir = cfg.FactorsDir
	}
	db, err := store.Open(&store.Options{Path: runtime.DatabasePath})
	if err != nil {
		return err
	}
	defer db.Close()
	storage, _, err := newStorageClients(runtime)
	if err != nil {
		return err
	}
	set, err := db.GetSet(ctx, cfg.SetID)
	if err != nil {
		return fmt.Errorf("get factor set: %w", err)
	}
	source, err := os.ReadFile(cfg.File)
	if err != nil {
		return fmt.Errorf("read factor source: %w", err)
	}
	factor, err := normalizeImportedFactor(cfg.SetID, cfg.FactorID, cfg.FactorType, cfg.File,
		string(source), cfg.InputColumns, cfg.Outputs, cfg.ParamsJSON, cfg.LookbackPeriods)
	if err != nil {
		return err
	}
	siblings, err := db.ListFactors(ctx, set.SetID, "")
	if err != nil {
		return err
	}
	sourceColumns, err := storage.DatasetColumns(ctx, set.SpaceID, set.SourceDatasetID)
	if err != nil {
		return fmt.Errorf("list source dataset columns: %w", err)
	}
	if err := domain.ValidateFactor(factor, sourceColumns, siblings); err != nil {
		return fmt.Errorf("validate factor: %w", err)
	}
	if _, err := (catalog.Artifacts{FactorsDir: runtime.FactorsDir}).Materialize(factor); err != nil {
		return fmt.Errorf("materialize factor source: %w", err)
	}
	if err := db.CreateFactor(ctx, factor); err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]any{
		"ok": true, "factor_id": factor.FactorID, "set_id": factor.SetID,
		"source_hash": factor.SourceHash, "status": factor.Status,
	})
}

func runImportCatalog(ctx context.Context, cfg cliConfig, out io.Writer) error {
	if cfg.SetID == "" {
		return errors.New("--set is required")
	}
	runtime, err := loadRuntimeConfig(cfg.ConfigPath)
	if err != nil {
		return err
	}
	if cfg.DBPath != "" {
		runtime.DatabasePath = cfg.DBPath
	}
	if cfg.FactorsDir != "" {
		runtime.FactorsDir = cfg.FactorsDir
	}
	catalogPath := filepath.Join(runtime.FactorsDir, "catalog.json")
	raw, err := os.ReadFile(catalogPath)
	if err != nil {
		return fmt.Errorf("read factor catalog: %w", err)
	}
	var entries []catalogEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return fmt.Errorf("decode factor catalog: %w", err)
	}
	if len(entries) == 0 {
		return errors.New("factor catalog is empty")
	}
	db, err := store.Open(&store.Options{Path: runtime.DatabasePath})
	if err != nil {
		return err
	}
	defer db.Close()
	storage, _, err := newStorageClients(runtime)
	if err != nil {
		return err
	}
	set, err := db.GetSet(ctx, cfg.SetID)
	if err != nil {
		return fmt.Errorf("get factor set: %w", err)
	}
	siblings, err := db.ListFactors(ctx, set.SetID, "")
	if err != nil {
		return err
	}
	sourceColumns, err := storage.DatasetColumns(ctx, set.SpaceID, set.SourceDatasetID)
	if err != nil {
		return fmt.Errorf("list source dataset columns: %w", err)
	}
	seenIDs := make(map[string]struct{}, len(entries))
	prepared := make([]domain.FactorDef, 0, len(entries))
	for _, entry := range entries {
		if entry.File == "" || filepath.Base(entry.File) != entry.File {
			return fmt.Errorf("factor %q has an invalid file path", entry.FactorID)
		}
		path := filepath.Join(runtime.FactorsDir, entry.File)
		source, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("read factor %s: %w", entry.FactorID, readErr)
		}
		params := entry.Params
		if params == nil {
			params = map[string]any{}
		}
		paramsJSON, marshalErr := json.Marshal(params)
		if marshalErr != nil {
			return fmt.Errorf("encode params for %s: %w", entry.FactorID, marshalErr)
		}
		factor, normalizeErr := normalizeImportedFactor(cfg.SetID, entry.FactorID, entry.FactorType,
			path, string(source), entry.InputColumns, entry.Outputs, string(paramsJSON), entry.LookbackPeriods)
		if normalizeErr != nil {
			return normalizeErr
		}
		if _, duplicate := seenIDs[factor.FactorID]; duplicate {
			return fmt.Errorf("duplicate factor catalog entry %q", factor.FactorID)
		}
		seenIDs[factor.FactorID] = struct{}{}
		for _, sibling := range siblings {
			if sibling.FactorID == factor.FactorID {
				return fmt.Errorf("factor %q already exists", factor.FactorID)
			}
		}
		if err := domain.ValidateFactor(factor, sourceColumns, append(siblings, prepared...)); err != nil {
			return fmt.Errorf("validate factor %s: %w", factor.FactorID, err)
		}
		prepared = append(prepared, factor)
	}
	for _, factor := range prepared {
		if _, err := (catalog.Artifacts{FactorsDir: runtime.FactorsDir}).Materialize(factor); err != nil {
			return fmt.Errorf("materialize factor %s: %w", factor.FactorID, err)
		}
	}
	for _, factor := range prepared {
		if err := db.CreateFactor(ctx, factor); err != nil {
			return fmt.Errorf("import factor %s: %w", factor.FactorID, err)
		}
	}
	imported := make([]map[string]string, 0, len(prepared))
	for _, factor := range prepared {
		imported = append(imported, map[string]string{"factor_id": factor.FactorID, "source_hash": factor.SourceHash, "status": factor.Status})
	}
	return json.NewEncoder(out).Encode(map[string]any{"ok": true, "imported": imported})
}

func normalizeImportedFactor(setID, factorID, factorType, sourcePath, source string, inputs, outputs []string, params string, lookback int) (domain.FactorDef, error) {
	if strings.ToLower(filepath.Ext(sourcePath)) != ".py" {
		return domain.FactorDef{}, errors.New("factor source file must end in .py")
	}
	name := strings.TrimSuffix(filepath.Base(sourcePath), filepath.Ext(sourcePath))
	factor, err := domain.NormalizeFactorDefinition(domain.FactorDef{
		SetID: setID, FactorID: factorID, Name: name, FactorType: factorType,
		SourceCode: source, InputColumns: inputs, Outputs: outputs,
		ParamsJSON: params, LookbackPeriods: lookback, Status: domain.FactorStatusDisabled,
	})
	if err != nil {
		return domain.FactorDef{}, err
	}
	factor.SourceHash = domain.SourceHash(factor.SourceCode)
	return factor, nil
}

func runRecalc(ctx context.Context, cli cliConfig, out io.Writer) error {
	config, err := loadRuntimeConfig(cli.ConfigPath)
	if err != nil {
		return err
	}
	if cli.DBPath != "" {
		config.DatabasePath = cli.DBPath
	}
	db, err := store.Open(&store.Options{Path: config.DatabasePath})
	if err != nil {
		return err
	}
	defer db.Close()
	requestID := cli.RequestID
	if requestID == "" {
		requestID, err = newRequestID()
		if err != nil {
			return err
		}
	}
	set, err := db.GetSet(ctx, cli.SetID)
	if err != nil {
		return fmt.Errorf("get factor set: %w", err)
	}
	options := []recalc.Option{recalc.WithClock(periodclock.Continuous{})}
	if len(cli.Subjects) == 0 && set.SubjectMode == domain.SubjectModeAll {
		_, subjects, clientErr := newStorageClients(config)
		if clientErr != nil {
			return clientErr
		}
		options = append(options, recalc.WithSubjectProvider(subjects))
	}
	svc := recalc.NewService(db, nil, options...)
	job, err := svc.Submit(ctx, cli.SetID, cli.FactorIDs, cli.Subjects, requestID, cli.StartTime, cli.EndTime)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]any{
		"ok": true, "job_id": job.JobID, "request_id": job.RequestID,
		"set_id": job.SetID, "status": job.Status,
		"start_time": time.Unix(job.StartTime, 0).UTC().Format(time.RFC3339),
		"end_time":   time.Unix(job.EndTime, 0).UTC().Format(time.RFC3339),
	})
}

func runOnce(ctx context.Context, cli cliConfig, out io.Writer) error {
	config, err := loadRuntimeConfig(cli.ConfigPath)
	if err != nil {
		return err
	}
	if cli.DBPath != "" {
		config.DatabasePath = cli.DBPath
	}
	if cli.FactorsDir != "" {
		config.FactorsDir = cli.FactorsDir
	}
	db, err := store.Open(&store.Options{Path: config.DatabasePath})
	if err != nil {
		return err
	}
	defer db.Close()
	storage, subjects, err := newStorageClients(config)
	if err != nil {
		return err
	}
	executor, err := pyexec.New(ctx, config.PythonWorkers, config.pythonProcess())
	if err != nil {
		return err
	}
	defer executor.Close()
	runner := pipeline.NewRunner(storage, executor, periodclock.Continuous{}, pipeline.Config{
		ReadWorkers: config.ReadWorkers, ReadTimeout: config.ReadTimeout,
		WriteBatchRows: config.WriteBatchRows, PythonWorkers: config.PythonWorkers,
		FactorsDir: config.FactorsDir,
	})
	worker := recalc.NewWorker(db, runner,
		recalc.WithClock(periodclock.Continuous{}),
		recalc.WithLocks(&catalog.Locks{}),
		recalc.WithChunkPeriods(1),
		recalc.WithColumnProvider(storage),
		recalc.WithSubjectProvider(subjects),
	)
	outcome, err := worker.RunOnce(ctx, cli.SetID, cli.FactorIDs, cli.Subjects, cli.Period)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]any{
		"ok": true, "set_id": cli.SetID, "period": cli.Period.UTC().Format(time.RFC3339Nano),
		"status": outcome.Status, "rows_written": outcome.RowsWritten, "factors": outcome.Factors,
	})
}

func runStatus(ctx context.Context, cli cliConfig, out io.Writer) error {
	if strings.TrimSpace(cli.RPCTarget) == "" {
		return errors.New("FactorMgr target is required: pass --target or set MOOX_FACTOR_RPC_TARGET")
	}
	api := factorgen.NewFactorMgrClientProxy(
		client.WithTarget(cli.RPCTarget), client.WithNetwork("tcp"), client.WithProtocol("trpc"),
	)
	rsp, err := api.GetStatus(ctx, &factorgen.GetStatusReq{})
	if err != nil {
		return fmt.Errorf("call FactorMgr.GetStatus: %w", err)
	}
	if rsp == nil || rsp.GetRetInfo() == nil {
		return errors.New("FactorMgr.GetStatus returned an empty response")
	}
	if rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
		return fmt.Errorf("FactorMgr.GetStatus failed: %s", rsp.GetRetInfo().GetMsg())
	}
	raw, err := protojson.Marshal(rsp)
	if err != nil {
		return fmt.Errorf("encode FactorMgr status: %w", err)
	}
	_, err = fmt.Fprintln(out, string(raw))
	return err
}

func newRequestID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate request id: %w", err)
	}
	return "cli-" + hex.EncodeToString(value[:]), nil
}

func cliAuthInfo() (*commonpb.AuthInfo, error) {
	requestID, err := newRequestID()
	if err != nil {
		return nil, err
	}
	auth := &commonpb.AuthInfo{AppId: "moox-factor", Operator: "moox-factor", RequestId: requestID}
	if secret := strings.TrimSpace(os.Getenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET")); secret != "" {
		auth.AppKey = mooxsecurity.HMACSHA256Hex(secret, []byte(auth.AppId))
	}
	return auth, nil
}

type cliSubjectProvider struct {
	metadata storagegen.MetadataClientProxy
	info     *commonpb.AuthInfo
}

func (p *cliSubjectProvider) ListDatasetSubjects(ctx context.Context, spaceID, datasetID string) ([]string, error) {
	if p == nil || p.metadata == nil {
		return nil, errors.New("Storage metadata client is required")
	}
	var subjects []string
	for page := uint32(1); ; page++ {
		rsp, err := p.metadata.ListDatasetSubjects(ctx, &storagegen.ListDatasetSubjectsReq{
			AuthInfo: p.info, SpaceId: spaceID, DatasetId: datasetID,
			Page: &commonpb.Page{Page: page, Size: 2000},
		})
		if err != nil {
			return nil, fmt.Errorf("list dataset subjects: %w", err)
		}
		if rsp == nil || rsp.GetRetInfo() == nil {
			return nil, errors.New("list dataset subjects returned an empty response")
		}
		if rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
			return nil, fmt.Errorf("list dataset subjects failed: %s", rsp.GetRetInfo().GetMsg())
		}
		for _, subject := range rsp.GetDatasetSubjects() {
			if subject == nil || subject.GetSubjectId() == "" || (subject.GetStatus() != "" && subject.GetStatus() != "active") {
				continue
			}
			subjects = append(subjects, subject.GetSubjectId())
		}
		result := rsp.GetPageResult()
		if result == nil || !result.GetHasMore() {
			break
		}
		if page >= 100000 {
			return nil, errors.New("Storage dataset subject pagination exceeded the page limit")
		}
	}
	return normalizeCLIList("Storage subjects", subjects)
}

func newStorageClients(cfg runtimeConfig) (*storageio.Client, *cliSubjectProvider, error) {
	credentials, err := gatewayauth.ResolveCredentials(cfg.KeyID, cfg.HMACKeyFile)
	if err != nil {
		return nil, nil, err
	}
	info, err := cliAuthInfo()
	if err != nil {
		return nil, nil, err
	}
	options := gatewayauth.NewTRPCClientOptions(cfg.GatewayTarget, cfg.GatewayNodeID, credentials)
	primary := storagegen.NewPrimaryStoreClientProxy(options...)
	metadata := storagegen.NewMetadataClientProxy(options...)
	return storageio.NewClient(primary, metadata, info), &cliSubjectProvider{metadata: metadata, info: info}, nil
}
