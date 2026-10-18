package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/factorlib"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/recalc"
	"github.com/mooyang-code/moox/modules/factor/internal/setlock"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorgen "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/mooyang-code/moox/packages/commonpb"
	"google.golang.org/protobuf/encoding/protojson"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/transport"
)

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

// runImport registers one definition read from a YAML factor library file.
// With --set it also attaches the definition to that factor set as a disabled
// member; enabling (which adds result columns and submits a backfill) goes
// through FactorMgr.
func runImport(ctx context.Context, cfg cliConfig, out io.Writer) error {
	if cfg.File == "" {
		return errors.New("--file is required")
	}
	entries, err := factorlib.LoadFile(cfg.File)
	if err != nil {
		return err
	}
	entry, err := selectEntry(entries, cfg.FactorID)
	if err != nil {
		return err
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
	unlock, err := lockImport(ctx, runtime.DatabasePath, []string{entry.FactorID}, cfg.SetID)
	if err != nil {
		return err
	}
	defer unlock()
	factor, err := entryDefinition(entry)
	if err != nil {
		return err
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
	result := map[string]any{"ok": true, "factor_id": factor.FactorID, "spec_hash": factor.SpecHash,
		"definition_hash": domain.DefinitionHash(factor)}
	if target != nil {
		if _, err := db.AddMember(ctx, target.set.SetID, factor.FactorID); err != nil {
			return fmt.Errorf("add factor to set: %w", err)
		}
		result["set_id"], result["member_status"] = target.set.SetID, domain.MemberStatusDisabled
	}
	return json.NewEncoder(out).Encode(result)
}

// selectEntry 从一个库文件里取出要导入的定义：文件只有一个定义时可以不指定 --factor-id。
func selectEntry(entries []factorlib.Entry, factorID string) (factorlib.Entry, error) {
	if factorID == "" {
		if len(entries) != 1 {
			return factorlib.Entry{}, fmt.Errorf("文件里有 %d 个定义，请用 --factor-id 指定", len(entries))
		}
		return entries[0], nil
	}
	for _, entry := range entries {
		if entry.FactorID == factorID {
			return entry, nil
		}
	}
	return factorlib.Entry{}, fmt.Errorf("文件里没有因子 %s", factorID)
}

// entryDefinition 把库里的定义规范化并编译。
func entryDefinition(entry factorlib.Entry) (domain.FactorDef, error) {
	factor, err := domain.NormalizeFactorDefinition(domain.FactorDef{
		FactorID: entry.FactorID, Name: entry.Name, Spec: entry.Spec, AllowPartialUniverse: entry.AllowPartialUniverse,
	})
	if err != nil {
		return domain.FactorDef{}, fmt.Errorf("因子 %s（%s）：%w", entry.FactorID, entry.File, err)
	}
	return factor, nil
}

// runImportCatalog registers every definition of the YAML factor library
// directory. The library itself carries no set; with --set the definitions are
// also attached to that set as disabled members.
func runImportCatalog(ctx context.Context, cfg cliConfig, out io.Writer) error {
	entries, err := factorlib.LoadDir(cfg.CatalogDir)
	if err != nil {
		return err
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
	prepared := make([]domain.FactorDef, 0, len(entries))
	for _, entry := range entries {
		if _, exists := known[entry.FactorID]; exists {
			return fmt.Errorf("factor %q already exists", entry.FactorID)
		}
		factor, err := entryDefinition(entry)
		if err != nil {
			return err
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
		item := map[string]string{"factor_id": factor.FactorID, "spec_hash": factor.SpecHash}
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
	sourceColumns []domain.SourceColumn
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
	columns, err := storage.ListColumns(ctx, set.SpaceID, set.SourceDatasetID)
	if err != nil {
		return nil, fmt.Errorf("list source dataset columns: %w", err)
	}
	sourceColumns := make([]domain.SourceColumn, 0, len(columns))
	for _, column := range columns {
		sourceColumns = append(sourceColumns, domain.SourceColumn{Name: column.ColumnName,
			Numeric: column.ValueType == storageio.ColumnTypeInt || column.ValueType == storageio.ColumnTypeDouble})
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
		client.WithTarget(cli.RPCTarget), client.WithNetwork("tcp"), client.WithProtocol("trpc"), client.WithTransport(transport.DefaultClientTransport),
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

func newStorageClient(cfg runtimeConfig) (*storageio.Client, func() error, error) {
	requestID, err := newRequestID()
	if err != nil {
		return nil, nil, err
	}
	if cfg.GatewayClient.Caller != "factor-mgr" {
		return nil, nil, errors.New("gateway_client.caller must be factor-mgr")
	}
	gateway, err := cfg.GatewayClient.OpenInternal(cfg.ConfigPath, filepath.Dir(cfg.DatabasePath), nil)
	if err != nil {
		return nil, nil, err
	}
	return storageio.NewGatewayClient(gateway, storageio.AuthInfo(requestID)), gateway.Close, nil
}
