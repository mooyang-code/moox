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
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pyexec"
	"github.com/mooyang-code/moox/modules/factor/internal/recalc"
	"github.com/mooyang-code/moox/modules/factor/internal/setlock"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorgen "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayclient"
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
		"tables": []string{"t_factor_sets", "t_factor_defs", "t_factor_set_members", "t_factor_recalc_jobs"},
	})
}

// runImport registers one definition. With --set it also attaches the
// definition to that factor set as a disabled member; enabling (which adds
// result columns and submits a backfill) goes through FactorMgr.
func runImport(ctx context.Context, cfg cliConfig, out io.Writer) error {
	if cfg.File == "" || cfg.FactorID == "" || len(cfg.InputColumns) == 0 || len(cfg.Outputs) == 0 || cfg.LookbackPeriods < 1 {
		return errors.New("--file, --factor-id, --inputs, --outputs and --lookback are required")
	}
	runtime, err := loadRuntimeConfig(cfg.ConfigPath)
	if err != nil {
		return err
	}
	if cfg.DBPath != "" {
		runtime.DatabasePath = cfg.DBPath
	}
	db, err := store.Open(&store.Options{Path: runtime.DatabasePath})
	if err != nil {
		return err
	}
	defer db.Close()
	unlock, err := lockImport(ctx, runtime.DatabasePath, []string{cfg.FactorID}, cfg.SetID)
	if err != nil {
		return err
	}
	defer unlock()
	source, err := os.ReadFile(cfg.File)
	if err != nil {
		return fmt.Errorf("read factor source: %w", err)
	}
	factor, err := normalizeImportedFactor(cfg.FactorID, cfg.FactorType, cfg.File,
		string(source), cfg.InputColumns, cfg.Outputs, cfg.ParamsJSON, cfg.LookbackPeriods)
	if err != nil {
		return err
	}
	if err := domain.ValidateDefinition(factor); err != nil {
		return fmt.Errorf("validate factor: %w", err)
	}
	if err := pyexec.ValidateSourceCode(ctx, runtime.PythonBin, string(source)); err != nil {
		return fmt.Errorf("load factor source: %w", err)
	}
	var target *importTarget
	if cfg.SetID != "" {
		target, err = loadImportTarget(ctx, db, runtime, cfg.SetID)
		if err != nil {
			return err
		}
		if err := target.validate(factor, nil); err != nil {
			return fmt.Errorf("validate factor for set %s: %w", cfg.SetID, err)
		}
	}
	if err := db.CreateFactor(ctx, factor); err != nil {
		return err
	}
	result := map[string]any{"ok": true, "factor_id": factor.FactorID, "source_hash": factor.SourceHash}
	if target != nil {
		if _, err := db.AddMember(ctx, target.set.SetID, factor.FactorID); err != nil {
			return fmt.Errorf("add factor to set: %w", err)
		}
		result["set_id"], result["member_status"] = target.set.SetID, domain.MemberStatusDisabled
	}
	return json.NewEncoder(out).Encode(result)
}

// runImportCatalog registers every definition in catalog.json. The catalog
// itself carries no set; with --set the definitions are also attached to that
// set as disabled members.
func runImportCatalog(ctx context.Context, cfg cliConfig, out io.Writer) error {
	runtime, err := loadRuntimeConfig(cfg.ConfigPath)
	if err != nil {
		return err
	}
	if cfg.DBPath != "" {
		runtime.DatabasePath = cfg.DBPath
	}
	catalogDir := cfg.CatalogDir
	catalogPath := filepath.Join(catalogDir, "catalog.json")
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
	factorIDs := make([]string, 0, len(entries))
	for _, entry := range entries {
		factorIDs = append(factorIDs, entry.FactorID)
	}
	unlock, err := lockImport(ctx, runtime.DatabasePath, factorIDs, cfg.SetID)
	if err != nil {
		return err
	}
	defer unlock()
	var target *importTarget
	if cfg.SetID != "" {
		target, err = loadImportTarget(ctx, db, runtime, cfg.SetID)
		if err != nil {
			return err
		}
	}
	existing, err := db.ListFactors(ctx)
	if err != nil {
		return err
	}
	known := make(map[string]struct{}, len(existing))
	for _, factor := range existing {
		known[factor.FactorID] = struct{}{}
	}
	seenIDs := make(map[string]struct{}, len(entries))
	prepared := make([]domain.FactorDef, 0, len(entries))
	for _, entry := range entries {
		if entry.File == "" || filepath.Base(entry.File) != entry.File {
			return fmt.Errorf("factor %q has an invalid file path", entry.FactorID)
		}
		path := filepath.Join(catalogDir, entry.File)
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
		factor, normalizeErr := normalizeImportedFactor(entry.FactorID, entry.FactorType,
			path, string(source), entry.InputColumns, entry.Outputs, string(paramsJSON), entry.LookbackPeriods)
		if normalizeErr != nil {
			return normalizeErr
		}
		if err := pyexec.ValidateSourceCode(ctx, runtime.PythonBin, string(source)); err != nil {
			return fmt.Errorf("load factor %s: %w", entry.FactorID, err)
		}
		if _, duplicate := seenIDs[factor.FactorID]; duplicate {
			return fmt.Errorf("duplicate factor catalog entry %q", factor.FactorID)
		}
		seenIDs[factor.FactorID] = struct{}{}
		if _, exists := known[factor.FactorID]; exists {
			return fmt.Errorf("factor %q already exists", factor.FactorID)
		}
		if err := domain.ValidateDefinition(factor); err != nil {
			return fmt.Errorf("validate factor %s: %w", factor.FactorID, err)
		}
		if target != nil {
			if err := target.validate(factor, prepared); err != nil {
				return fmt.Errorf("validate factor %s for set %s: %w", factor.FactorID, cfg.SetID, err)
			}
		}
		prepared = append(prepared, factor)
	}
	if err := db.CreateFactors(ctx, prepared); err != nil {
		return fmt.Errorf("import factor catalog: %w", err)
	}
	imported := make([]map[string]string, 0, len(prepared))
	for _, factor := range prepared {
		item := map[string]string{"factor_id": factor.FactorID, "source_hash": factor.SourceHash}
		if target != nil {
			if _, err := db.AddMember(ctx, target.set.SetID, factor.FactorID); err != nil {
				return fmt.Errorf("add factor %s to set: %w", factor.FactorID, err)
			}
			item["member_status"] = domain.MemberStatusDisabled
		}
		imported = append(imported, item)
	}
	result := map[string]any{"ok": true, "imported": imported}
	if target != nil {
		result["set_id"] = target.set.SetID
	}
	return json.NewEncoder(out).Encode(result)
}

// importTarget is the set (and its source columns) that imported definitions are attached to.
type importTarget struct {
	set           domain.FactorSet
	sourceColumns []string
	siblings      []domain.FactorDef
}

func loadImportTarget(ctx context.Context, db *store.Store, runtime runtimeConfig, setID string) (*importTarget, error) {
	storage, closeStorage, err := newStorageClient(runtime)
	if err != nil {
		return nil, err
	}
	defer closeStorage()
	set, err := db.GetSet(ctx, setID)
	if err != nil {
		return nil, fmt.Errorf("get factor set: %w", err)
	}
	if err := validateImportableSet(set); err != nil {
		return nil, err
	}
	members, err := db.ListMembers(ctx, set.SetID, "")
	if err != nil {
		return nil, err
	}
	siblings := make([]domain.FactorDef, 0, len(members))
	for _, member := range members {
		siblings = append(siblings, member.Factor)
	}
	sourceColumns, err := storage.DatasetColumns(ctx, set.SpaceID, set.SourceDatasetID)
	if err != nil {
		return nil, fmt.Errorf("list source dataset columns: %w", err)
	}
	return &importTarget{set: set, sourceColumns: sourceColumns, siblings: siblings}, nil
}

// validate runs the dataset-dependent membership checks; result-dataset column
// ownership is checked later when FactorMgr enables the member.
func (t *importTarget) validate(factor domain.FactorDef, pending []domain.FactorDef) error {
	siblings := append(append([]domain.FactorDef(nil), t.siblings...), pending...)
	return domain.ValidateMembership(t.set, factor, t.sourceColumns, siblings, nil)
}

func validateImportableSet(set domain.FactorSet) error {
	switch set.Status {
	case domain.SetStatusEnabled, domain.SetStatusDisabled:
		return nil
	case domain.SetStatusDeleting:
		return errors.New("factor set purge is pending")
	default:
		return errors.New("factor set must be enabled or disabled before importing factors")
	}
}

// lockImport takes the definition locks (ascending) and then the optional set
// lock, the same order the catalog service uses.
func lockImport(ctx context.Context, databasePath string, factorIDs []string, setID string) (func(), error) {
	locks := setlock.New(databasePath + ".locks")
	ids := append([]string(nil), factorIDs...)
	sort.Strings(ids)
	var unlocks []func()
	release := func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
	}
	for i, id := range ids {
		if i > 0 && id == ids[i-1] {
			continue
		}
		unlock, err := locks.LockFactorContext(ctx, id)
		if err != nil {
			release()
			return nil, err
		}
		unlocks = append(unlocks, unlock)
	}
	if setID != "" {
		unlock, err := locks.LockContext(ctx, setID)
		if err != nil {
			release()
			return nil, err
		}
		unlocks = append(unlocks, unlock)
	}
	return release, nil
}

func normalizeImportedFactor(factorID, factorType, sourcePath, source string, inputs, outputs []string, params string, lookback int) (domain.FactorDef, error) {
	if strings.ToLower(filepath.Ext(sourcePath)) != ".py" {
		return domain.FactorDef{}, errors.New("factor source file must end in .py")
	}
	name := strings.TrimSuffix(filepath.Base(sourcePath), filepath.Ext(sourcePath))
	factor, err := domain.NormalizeFactorDefinition(domain.FactorDef{
		FactorID: factorID, Name: name, FactorType: factorType,
		SourceCode: source, InputColumns: inputs, Outputs: outputs,
		ParamsJSON: params, LookbackPeriods: lookback,
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
		storage, closeStorage, clientErr := newStorageClient(config)
		if clientErr != nil {
			return clientErr
		}
		defer closeStorage()
		options = append(options, recalc.WithSubjectProvider(storage))
	}
	svc := recalc.NewService(db, options...)
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

// newStorageClient 以 factor-mgr 身份经本机主机网关访问 Storage；用完调用返回的 close。
func newStorageClient(cfg runtimeConfig) (*storageio.Client, func(), error) {
	gateway, err := gatewayclient.New(gatewayclient.Options{Config: cfg.GatewayClient})
	if err != nil {
		return nil, nil, fmt.Errorf("创建 gatewayclient: %w", err)
	}
	requestID, err := newRequestID()
	if err != nil {
		gateway.Close()
		return nil, nil, err
	}
	return storageio.NewClientWithOptions(gateway.ClientOptions(), storageio.AuthInfo(requestID)), gateway.Close, nil
}
