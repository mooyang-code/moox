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
		"tables": []string{"t_factor_tasks", "t_factor_defs", "t_factor_task_members", "t_factor_recalc_jobs"},
	})
}

// runImport registers one definition read from a YAML factor library file.
// With --task it also attaches the definition to that factor task as a disabled
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
	unlock, err := lockImport(ctx, runtime.DatabasePath, []string{entry.FactorID}, cfg.TaskID)
	if err != nil {
		return err
	}
	defer unlock()
	factor, err := entryDefinition(entry)
	if err != nil {
		return err
	}
	var target *importTarget
	if cfg.TaskID != "" {
		target, err = loadImportTarget(ctx, db, runtime, cfg.TaskID)
		if err != nil {
			return err
		}
		if err := target.validate(factor, nil); err != nil {
			return fmt.Errorf("validate factor for set %s: %w", cfg.TaskID, err)
		}
	}
	if err := ensureCategories(ctx, db, []factorlib.Entry{entry}); err != nil {
		return err
	}
	if err := db.CreateFactor(ctx, factor); err != nil {
		return err
	}
	result := map[string]any{"ok": true, "factor_id": factor.FactorID, "spec_hash": factor.SpecHash,
		"definition_hash": domain.DefinitionHash(factor)}
	if target != nil {
		if err := addDefaultMember(ctx, db, target.set.TaskID, factor); err != nil {
			return fmt.Errorf("add factor to task: %w", err)
		}
		result["task_id"], result["member_status"] = target.set.TaskID, domain.MemberStatusDisabled
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

// ensureCategories 建立或更新因子库里声明了名称的分类。
func ensureCategories(ctx context.Context, db *store.Store, entries []factorlib.Entry) error {
	for _, entry := range entries {
		if entry.CategoryID == "" || entry.CategoryName == "" {
			continue
		}
		if _, err := db.UpsertCategory(ctx, domain.FactorCategory{CategoryID: entry.CategoryID, Name: entry.CategoryName}); err != nil {
			return fmt.Errorf("因子 %s 的分类 %s：%w", entry.FactorID, entry.CategoryID, err)
		}
	}
	return nil
}

// entryDefinition 把库里的定义规范化并编译。
func entryDefinition(entry factorlib.Entry) (domain.FactorDef, error) {
	factor, err := domain.NormalizeFactorDefinition(domain.FactorDef{
		FactorID: entry.FactorID, Name: entry.Name, CategoryID: entry.CategoryID, Spec: entry.Spec, AllowPartialUniverse: entry.AllowPartialUniverse,
	})
	if err != nil {
		return domain.FactorDef{}, fmt.Errorf("因子 %s（%s）：%w", entry.FactorID, entry.File, err)
	}
	return factor, nil
}

// runImportCatalog registers every definition of the YAML factor library
// directory. The library itself carries no set; with --task the definitions are
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
	unlock, err := lockImport(ctx, runtime.DatabasePath, factorIDs, cfg.TaskID)
	if err != nil {
		return err
	}
	defer unlock()
	var target *importTarget
	if cfg.TaskID != "" {
		target, err = loadImportTarget(ctx, db, runtime, cfg.TaskID)
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
				return fmt.Errorf("validate factor %s for set %s: %w", factor.FactorID, cfg.TaskID, err)
			}
		}
		prepared = append(prepared, factor)
	}
	if err := ensureCategories(ctx, db, entries); err != nil {
		return err
	}
	if err := db.CreateFactors(ctx, prepared); err != nil {
		return fmt.Errorf("import factor catalog: %w", err)
	}
	imported := make([]map[string]string, 0, len(prepared))
	for _, factor := range prepared {
		item := map[string]string{"factor_id": factor.FactorID, "spec_hash": factor.SpecHash}
		if target != nil {
			if err := addDefaultMember(ctx, db, target.set.TaskID, factor); err != nil {
				return fmt.Errorf("add factor %s to task: %w", factor.FactorID, err)
			}
			item["member_status"] = domain.MemberStatusDisabled
		}
		imported = append(imported, item)
	}
	result := map[string]any{"ok": true, "imported": imported}
	if target != nil {
		result["task_id"] = target.set.TaskID
	}
	return json.NewEncoder(out).Encode(result)
}

// importTarget is the set (and its source columns) that imported definitions are attached to.
type importTarget struct {
	set           domain.FactorTask
	sourceColumns []domain.SourceColumn
	siblings      []domain.MemberDef
}

func loadImportTarget(ctx context.Context, db *store.Store, runtime runtimeConfig, taskID string) (*importTarget, error) {
	storage, closeStorage, err := newStorageClient(runtime)
	if err != nil {
		return nil, err
	}
	defer closeStorage()
	set, err := db.GetTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("get factor task: %w", err)
	}
	if err := validateImportableTask(set); err != nil {
		return nil, err
	}
	members, err := db.ListMembers(ctx, set.TaskID, "")
	if err != nil {
		return nil, err
	}
	siblings := make([]domain.MemberDef, 0, len(members))
	for _, member := range members {
		siblings = append(siblings, member.Def())
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

// validate 对照源数据集做成员校验：导入的成员都是参数全取默认值的固定成员（成员键就是因子 ID）；
// 结果数据集的列归属在 FactorMgr 启用成员时再校验。
func (t *importTarget) validate(factor domain.FactorDef, pending []domain.FactorDef) error {
	siblings := append([]domain.MemberDef(nil), t.siblings...)
	for _, def := range pending {
		siblings = append(siblings, domain.MemberDef{Key: def.FactorID, Factor: def, Kind: domain.MemberKindFixed})
	}
	return domain.ValidateMembership(t.set, domain.MemberDef{Key: factor.FactorID, Factor: factor, Kind: domain.MemberKindFixed}, t.sourceColumns, siblings, nil)
}

func validateImportableTask(set domain.FactorTask) error {
	switch set.Status {
	case domain.TaskStatusEnabled, domain.TaskStatusDisabled:
		return nil
	default:
		return errors.New("factor task must be enabled or disabled before importing factors")
	}
}

// lockImport takes the definition locks (ascending) and then the optional set
// lock, the same order the catalog service uses.
func lockImport(ctx context.Context, databasePath string, factorIDs []string, taskID string) (func(), error) {
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
	if taskID != "" {
		unlock, err := locks.LockContext(ctx, taskID)
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
	// 任务的标的就是源数据集当前的标的：指定或不指定标的都要查询源数据集成员。
	storage, closeStorage, clientErr := newStorageClient(config)
	if clientErr != nil {
		return clientErr
	}
	defer closeStorage()
	svc := recalc.NewService(db, recalc.WithClock(periodclock.Continuous{}), recalc.WithSubjectProvider(storage))
	job, err := svc.Submit(ctx, cli.TaskID, cli.Members, cli.Subjects, requestID, cli.StartTime, cli.EndTime)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]any{
		"ok": true, "job_id": job.JobID, "request_id": job.RequestID,
		"task_id": job.TaskID, "status": job.Status,
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

// runPurgeDataset 删除一个因子结果数据集（行与元数据）：用于清理已经没有因子任务引用的孤立结果数据集
// （例如重建因子库后遗留的旧结果）。仍被因子任务引用的数据集会被拒绝；只允许因子结果数据集。
func runPurgeDataset(ctx context.Context, cli cliConfig, out io.Writer) error {
	if !cli.Yes {
		return errors.New("purge-dataset permanently deletes the dataset and its rows; re-run with --yes")
	}
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
	sets, err := db.ListTasks(ctx)
	if err != nil {
		return err
	}
	for _, set := range sets {
		if set.SpaceID == cli.SpaceID && set.ResultDatasetID == cli.DatasetID {
			return fmt.Errorf("dataset %s is still the result of factor task %s", cli.DatasetID, set.TaskID)
		}
	}
	storage, closeStorage, err := newStorageClient(config)
	if err != nil {
		return err
	}
	defer closeStorage()
	if err := storage.DeleteDataset(ctx, cli.SpaceID, cli.DatasetID); err != nil {
		return fmt.Errorf("purge dataset %s: %w", cli.DatasetID, err)
	}
	return json.NewEncoder(out).Encode(map[string]any{"ok": true, "space_id": cli.SpaceID, "dataset_id": cli.DatasetID})
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

// addDefaultMember 把定义作为参数全取默认值的固定成员加入任务（成员键就是因子 ID）。
func addDefaultMember(ctx context.Context, db *store.Store, taskID string, factor domain.FactorDef) error {
	params, err := domain.ResolveMemberParams(factor, nil)
	if err != nil {
		return err
	}
	key, err := domain.DeriveMemberKey(factor, domain.MemberKindFixed, params)
	if err != nil {
		return err
	}
	_, err = db.AddMember(ctx, taskID, key, factor.FactorID, domain.MemberKindFixed, params)
	return err
}
