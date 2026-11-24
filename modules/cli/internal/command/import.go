package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/seriesformat"
	"github.com/mooyang-code/moox/modules/cli/internal/seriesimport/checkpoint"
	"github.com/mooyang-code/moox/modules/cli/internal/seriesimport/runner"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/spf13/cobra"
)

const importStorageBatchRows = 1000

var importFlags importOptions

// importOptions 保存导入命令的参数。
type importOptions struct {
	Space, Dataset, Freq string
	Paths                []string
	Subject              string
	TimeColumn           string
	SeriesTag            string
	BatchRows            int
	AllowMissing         bool
	Recalc               bool
	DryRun               bool
	Targets              []string
	Manifest             string
	ViewID               string
	JobDB                string
	JobID                string
	// TailRows 与 Pack、MinRows 见初始数据导入执行计划：Storage 只写每个文件最后 TailRows 行（且在保留期内）、
	// 按导入包清单预检、要求每个文件至少 MinRows 行。
	TailRows int
	Pack     string
	MinRows  int
}

var importCmd = &cobra.Command{
	Use:   "import",
	Short: "导入历史时序数据（写 Storage、因子机本地存储与归档）",
	Long: `把历史时序数据文件（.csv 或 .csv.gz）导入 Storage、因子机的本地时序存储与归档。

文件必须带数据集的全部字段和时间列（可选 series_tag 列），时间按 UTC 解析；文件名（去掉 .csv 或 .csv.gz）即标的 ID。
本地存储按频率网格保证连续：序列内部缺周期时默认拒绝，加 --allow-missing 把缺的周期写成带标记的填充行。
写 Storage 与写因子机本地存储都经过操作员 SSH 网关（同 moox-cli storage 的其他命令）：因子机本地存储经因子引擎
trpc.moox.factor.EngineControl 的 WriteSeries 写入，因子机只能出站时由网关经 control 中转，不需要手工建隧道。
一批（--batch-rows）编码后不能超过 4 MiB，默认一万行远在这之内；超了会提示减小 --batch-rows。

写入目标 archive（归档）：只用于线上保留期之外的历史（保留期内的数据写进 Storage 就会被归档跟随）。
命令建一条占住归档任务的导入运行，行经 worker 数据端口（只出站主机经 control 中转，较慢）写进暂存区，命令结束时统一导出；
series_tag 必须非空（列或 --series-tag）；同键已归档的一律覆盖；预检按行数估算 worker 暂存库要的磁盘，不够时按时间拆成几次导入。
只写归档时接受带 subject_id 列的多标的文件，且不检查标的在不在数据集的标签范围（摘要列出不在范围内的标的）。

示例:
  moox-cli import --space-id crypto --dataset-id dataset_spot_kline_1m --frequency 1m \
    --path ~/Downloads/spot_1m/ --target storage,factor --allow-missing --recalc

  moox-cli import --space-id crypto --dataset-id dataset_spot_kline_1m --frequency 1m --path ~/Downloads/BTCUSDT.csv --dry-run

  moox-cli import --space-id crypto --dataset-id dataset_spot_kline_1m --frequency 1m \
    --path ~/Downloads/spot_1m_2024.csv.gz --target archive --series-tag spot --allow-missing`,
	RunE: func(cmd *cobra.Command, args []string) error { return runImportCommand(cmd.Context(), importFlags) },
}

func init() {
	rootCmd.AddCommand(importCmd)
	f := importCmd.Flags()
	f.StringVar(&importFlags.Space, "space-id", "", "Space ID")
	f.StringVar(&importFlags.Dataset, "dataset-id", "", "Dataset ID")
	f.StringVar(&importFlags.Freq, "frequency", "", "时序频率：1m 5m 15m 30m 1h 4h 1d")
	f.StringArrayVar(&importFlags.Paths, "path", nil, "数据文件或目录（可重复）")
	f.StringVar(&importFlags.Subject, "subject", "", "标的 ID；不写时用文件名（去掉 .csv 或 .csv.gz）")
	f.StringVar(&importFlags.TimeColumn, "time-column", "data_time", "时间列名")
	f.StringVar(&importFlags.SeriesTag, "series-tag", "", "统一指定 series_tag；为空表示默认序列（文件里有 series_tag 列时按文件）")
	f.IntVar(&importFlags.BatchRows, "batch-rows", 10000, "每批最多行数（不会拆开同一个时间点）")
	f.BoolVar(&importFlags.AllowMissing, "allow-missing", false, "允许序列内部缺周期（交易所本身缺数据），因子本地存储把缺的周期写成带标记的填充行")
	f.BoolVar(&importFlags.Recalc, "recalc", false, "导入后对已算出过因子值的区间重算（因子本地存储记入失效重算待办）")
	f.BoolVar(&importFlags.DryRun, "dry-run", false, "只校验不写入")
	f.StringSliceVar(&importFlags.Targets, "target", []string{"storage", "factor"}, "写入目标：storage、factor、archive（可任意组合）")
	f.StringVar(&importFlags.Manifest, "file", "./moox.toml", "操作员 SSH 网关使用的部署配置文件")
	f.StringVar(&importFlags.ViewID, "view", "", "可选 View ID；写 Storage 后等该 View 跟上导入的写入")
	f.StringVar(&importFlags.JobDB, "job-db", "./importer.db", "断点库路径")
	f.StringVar(&importFlags.JobID, "job", "", "任务 ID；不写时按请求与文件特征生成，重跑同一请求自动续传")
	f.IntVar(&importFlags.TailRows, "tail-rows", 0, "Storage 目标每个文件只写最后 N 行，且跳过早于保留期的行；因子机本地目标仍写全量（初始数据导入用，0 表示不限制）")
	f.StringVar(&importFlags.Pack, "pack", "", "导入包目录（含 manifest.json）：空间、数据集、频率、series_tag 与文件清单取自清单，并按清单预检；--dry-run 只预检")
	f.IntVar(&importFlags.MinRows, "min-rows", 0, "预检要求每个文件至少这么多行（该数据源的回看数量，保证因子窗口凑得满）")
}

func runImportCommand(ctx context.Context, opts importOptions) error {
	var pack *importPack
	if opts.Pack != "" {
		var err error
		if pack, err = applyImportPack(&opts); err != nil {
			return err
		}
	}
	step, err := importStep(opts.Freq)
	if err != nil {
		return err
	}
	wantStorage, wantFactor, wantArchive, err := importTargets(opts.Targets)
	if err != nil {
		return err
	}
	archiveOnly := wantArchive && !wantStorage && !wantFactor
	gateway, err := openCommandGateway(ctx, opts.Manifest, nil)
	if err != nil {
		return err
	}
	defer gateway.Close()
	if wantFactor && !deployedService(gateway, engineControl) {
		return errors.New("服务目录里没有启用的因子引擎（factor-engine）：--target factor 要写因子机本地存储，先部署因子引擎，或只用 --target storage")
	}
	engine := factorEngine{Gateway: gateway}

	var (
		meta    runner.Meta
		targets []runner.Target
	)
	if wantStorage || wantArchive {
		metaClient := gatewayStorageImportMetadataClient{Gateway: gateway}
		dataset, err := metaClient.GetDataset(ctx, opts.Space, opts.Dataset)
		if err != nil {
			return err
		}
		if dataset == nil {
			return fmt.Errorf("dataset %s/%s not found", opts.Space, opts.Dataset)
		}
		if dataset.GetFreq() != "" && dataset.GetFreq() != opts.Freq {
			return fmt.Errorf("dataset %s freq is %s, not %s", opts.Dataset, dataset.GetFreq(), opts.Freq)
		}
		columns, err := metaClient.ListDatasetColumns(ctx, opts.Space, opts.Dataset)
		if err != nil {
			return err
		}
		fields, types, err := importFieldsFromColumns(columns)
		if err != nil {
			return err
		}
		meta = runner.Meta{Fields: fields}
		switch {
		case wantStorage:
			meta.SubjectInScope = func(ctx context.Context, subject string) (bool, error) {
				subjects, err := metaClient.ListDatasetSubjects(ctx, opts.Space, opts.Dataset, subject)
				if err != nil {
					return false, err
				}
				return storageImportSubjectBound(subjects, subject), nil
			}
		case wantFactor:
			meta.SubjectInScope = func(ctx context.Context, subject string) (bool, error) {
				return engine.SubjectInScope(ctx, opts.Space, opts.Dataset, subject)
			}
		default:
			// 只写归档：归档存长历史，已下架、已移出标签的标的仍要能补进归档；不在范围内的只列进摘要。
			if meta.SubjectInScope, err = archiveSubjectScope(ctx, metaClient, opts.Space, opts.Dataset); err != nil {
				return err
			}
			meta.ScopeWarnOnly = true
		}
		if wantArchive {
			check, err := checkArchiveImport(ctx, gateway, opts.Space, opts.Dataset)
			if err != nil {
				return err
			}
			meta.BeforeWrite = func(ctx context.Context, summary runner.Summary) error {
				return checkArchiveDisk(ctx, gateway, opts.Space, opts.Dataset, check, summary, len(fields))
			}
		}
		if opts.ViewID != "" {
			if err := checkImportView(ctx, metaClient, opts.Space, opts.ViewID, opts.Dataset); err != nil {
				return err
			}
		}
		if wantStorage && !opts.DryRun {
			writer := gatewayStorageDataWriter{Gateway: gateway, SpaceID: opts.Space}
			targets = append(targets, &importStorageTarget{writer: writer, types: types, view: opts.ViewID, space: opts.Space, dataset: opts.Dataset})
		}
		if wantArchive && !opts.DryRun {
			targets = append(targets, &importArchiveTarget{gateway: gateway, file: opts.Manifest, space: opts.Space, dataset: opts.Dataset, types: types})
		}
	} else {
		fields, err := engine.Fields(ctx, opts.Space, opts.Dataset, opts.Freq)
		if err != nil {
			return fmt.Errorf("读取数据集字段：%w", err)
		}
		meta = runner.Meta{Fields: fields, SubjectInScope: func(ctx context.Context, subject string) (bool, error) {
			return engine.SubjectInScope(ctx, opts.Space, opts.Dataset, subject)
		}}
	}
	if wantFactor {
		meta.LocalBounds = engine.LocalBounds(opts.Space, opts.Dataset, opts.Freq)
	}
	if wantFactor && !opts.DryRun {
		targets = append(targets, newFactorTarget(engine))
	}
	retention, retentionKnown := loadImportRetention(opts.Manifest, opts.Space, opts.Freq)
	limits := importPackLimits{Now: time.Now().UTC(), Retention: retention, RetentionKnown: retentionKnown, MinRows: opts.MinRows}
	// 用了 --tail-rows 的 Storage 目标只写保留期内的尾部，不会有“写进去又被清掉”的行，不需要保留期警告。
	if wantStorage && opts.TailRows == 0 && retentionKnown {
		warnImportRetention(os.Stderr, opts.Paths, opts.TimeColumn, opts.Space, opts.Freq, retention, time.Now())
	}
	cps, err := checkpoint.Open(opts.JobDB)
	if err != nil {
		return err
	}
	defer cps.Close()
	request := runner.Request{
		Space: opts.Space, Dataset: opts.Dataset, Freq: opts.Freq, Paths: opts.Paths, Subject: opts.Subject,
		SubjectColumn: archiveOnly && opts.Subject == "", RequireSeriesTag: wantArchive,
		TimeColumn: opts.TimeColumn, SeriesTag: opts.SeriesTag, BatchRows: opts.BatchRows, AllowMissing: opts.AllowMissing,
		Recalc: opts.Recalc, DryRun: opts.DryRun, JobID: opts.JobID, Step: step,
		TailRows: opts.TailRows, TailCutoff: limits.tailCutoff(),
	}
	if pack != nil {
		// 导入包先整体预检（校验全部文件、核对清单与硬约束），通过了才写；--dry-run 到这里为止。
		preflight := request
		preflight.DryRun = true
		checked, err := runner.Run(ctx, preflight, meta, nil, nil)
		problems := checkImportPackFiles(pack)
		if err == nil {
			problems = append(problems, checkImportPack(pack, checked, limits)...)
		}
		if err != nil || len(problems) > 0 {
			return importPackPreflightError(err, problems)
		}
		if opts.DryRun {
			return writeImportSummary(checked)
		}
	}
	summary, runErr := runner.Run(ctx, request, meta, targets, cps)
	if err := writeImportSummary(summary); err != nil && runErr == nil {
		runErr = err
	}
	return runErr
}

func writeImportSummary(summary runner.Summary) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(summary)
}

// applyImportPack 从导入包清单补全空间、数据集、频率、series_tag（文件可单独指定）与文件清单；命令行同时给了不一致的值就拒绝。
func applyImportPack(opts *importOptions) (*importPack, error) {
	pack, err := loadImportPack(opts.Pack)
	if err != nil {
		return nil, err
	}
	for _, field := range []struct {
		name, flag, manifest string
		target               *string
	}{
		{"space-id", opts.Space, pack.SpaceID, &opts.Space},
		{"dataset-id", opts.Dataset, pack.DatasetID, &opts.Dataset},
		{"frequency", opts.Freq, pack.Freq, &opts.Freq},
		{"series-tag", opts.SeriesTag, pack.SeriesTag, &opts.SeriesTag},
	} {
		if field.flag != "" && field.flag != field.manifest {
			return nil, fmt.Errorf("--%s=%q 与导入包清单的 %q 不一致", field.name, field.flag, field.manifest)
		}
		*field.target = field.manifest
	}
	if len(opts.Paths) > 0 || opts.Subject != "" {
		return nil, errors.New("用了 --pack 就不要再指定 --path 或 --subject：文件清单取自导入包")
	}
	if opts.MinRows <= 0 {
		return nil, errors.New("用了 --pack 必须同时给 --min-rows（该数据源的回看数量，见因子引擎 engine.yaml：7×24 市场 window.lookback，有交易时段的市场 window.session_lookback，有 lookback_overrides 时取覆盖值）：行数不够时因子窗口凑不满，这是预检的硬约束")
	}
	opts.Paths = pack.paths()
	return pack, nil
}

func importPackPreflightError(runErr error, problems []string) error {
	var lines []string
	if runErr != nil {
		lines = append(lines, runErr.Error())
	}
	lines = append(lines, problems...)
	shown := lines
	if len(shown) > 20 {
		shown = append(append([]string(nil), shown[:20]...), fmt.Sprintf("……另有 %d 项", len(lines)-20))
	}
	return fmt.Errorf("导入包预检未通过（%d 项，没有写入任何内容）：\n  %s", len(lines), strings.Join(shown, "\n  "))
}

func importTargets(names []string) (storage, factor, archive bool, err error) {
	for _, name := range names {
		switch strings.TrimSpace(name) {
		case "storage":
			storage = true
		case "factor":
			factor = true
		case "archive":
			archive = true
		case "":
		default:
			return false, false, false, fmt.Errorf("未知的写入目标 %q（storage、factor、archive）", name)
		}
	}
	if !storage && !factor && !archive {
		return false, false, false, errors.New("至少要有一个写入目标（--target storage,factor,archive）")
	}
	return storage, factor, archive, nil
}

func importStep(freq string) (time.Duration, error) {
	steps := map[string]time.Duration{"1m": time.Minute, "5m": 5 * time.Minute, "15m": 15 * time.Minute, "30m": 30 * time.Minute,
		"1h": time.Hour, "4h": 4 * time.Hour, "1d": 24 * time.Hour}
	if d, ok := steps[freq]; ok {
		return d, nil
	}
	return 0, fmt.Errorf("不支持的频率 %q（支持 1m 5m 15m 30m 1h 4h 1d）", freq)
}

// importFieldsFromColumns 取数据集的启用字段：来源为字段或自定义（与归档 worker 取列的口径一致，否则归档批次会被拒），
// 不含时间、标的、series_tag 等保留列，按名称排序。
func importFieldsFromColumns(columns []*pb.DatasetColumn) ([]seriesformat.Field, map[string]pb.FieldValueType, error) {
	byName, err := storageImportColumnMap(columns)
	if err != nil {
		return nil, nil, err
	}
	reserved := map[string]struct{}{"subject_id": {}, "freq": {}, "data_time": {}, "series_tag": {}}
	var fields []seriesformat.Field
	types := map[string]pb.FieldValueType{}
	for name, column := range byName {
		if column.GetStatus() != "" && column.GetStatus() != "active" {
			continue
		}
		switch column.GetOriginType() {
		case pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_CUSTOM:
		default:
			continue
		}
		if _, skip := reserved[name]; skip {
			continue
		}
		fields = append(fields, seriesformat.Field{Name: name, Type: importFieldType(column.GetValueType())})
		types[name] = column.GetValueType()
	}
	if len(fields) == 0 {
		return nil, nil, errors.New("数据集没有可导入的字段")
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
	return fields, types, nil
}

func importFieldType(t pb.FieldValueType) seriesformat.FieldType {
	switch t {
	case pb.FieldValueType_FIELD_VALUE_TYPE_INT:
		return seriesformat.TypeInt
	case pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE:
		return seriesformat.TypeDouble
	case pb.FieldValueType_FIELD_VALUE_TYPE_BOOL:
		return seriesformat.TypeBool
	case pb.FieldValueType_FIELD_VALUE_TYPE_TIME:
		return seriesformat.TypeTime
	case pb.FieldValueType_FIELD_VALUE_TYPE_JSON:
		return seriesformat.TypeJSON
	case pb.FieldValueType_FIELD_VALUE_TYPE_BYTES:
		return seriesformat.TypeBytes
	default:
		return seriesformat.TypeString
	}
}

// importStorageTarget 把一批行以字段更新（UpsertFields）写进 Storage，写完后可选地等 View 跟上。
type importStorageTarget struct {
	writer  gatewayStorageDataWriter
	types   map[string]pb.FieldValueType
	view    string
	space   string
	dataset string
	written bool
	subject map[string]struct{}
}

func (t *importStorageTarget) Name() string { return "storage" }

// TailOnly：Storage 只放近期窗口，--tail-rows 只对它生效；因子机本地写全量。
func (t *importStorageTarget) TailOnly() bool { return true }

func (t *importStorageTarget) Write(ctx context.Context, c runner.Chunk) error {
	t.written = true
	if t.subject == nil {
		t.subject = map[string]struct{}{}
	}
	t.subject[c.Subject] = struct{}{}
	rows, err := importUpserts(c, t.types)
	if err != nil {
		return err
	}
	for start := 0; start < len(rows); start += importStorageBatchRows {
		end := min(start+importStorageBatchRows, len(rows))
		if err := t.writer.UpsertFields(ctx, &pb.PrimaryUpsertFieldsReq{SpaceId: t.space, DatasetId: t.dataset, Rows: rows[start:end]}); err != nil {
			return err
		}
	}
	return nil
}

// importViewWaitTimeout 是导入结束后等指定 View 跟上写入的总时长；View 正在建索引（VIEW_NOT_READY）时每隔 importViewRetryInterval 再问一次。
const (
	importViewWaitTimeout   = 30 * time.Second
	importViewRetryInterval = 200 * time.Millisecond
)

// Finish 指定了 View 时等它跟上导入的写入：View 在服务端取当前变更日志头部，扫描到它时导入的行都已可查。
func (t *importStorageTarget) Finish(ctx context.Context) error {
	if !t.written || t.view == "" {
		return nil
	}
	deadline := time.Now().Add(importViewWaitTimeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("View %s 在 %s 内没有跟上导入的写入", t.view, importViewWaitTimeout)
		}
		rsp, err := t.writer.WaitViewApplied(ctx, &pb.WaitViewAppliedReq{SpaceId: t.space, ViewId: t.view, WaitTimeoutMs: uint32(max(remaining/time.Millisecond, 1))})
		if err != nil {
			return fmt.Errorf("等待 View 跟上导入的写入：%w", err)
		}
		switch {
		case rsp.GetRetInfo() == nil:
			return errors.New("等待 View 跟上导入的写入：响应缺少 ret_info")
		case rsp.GetRetInfo().GetCode() == pb.ErrorCode_SUCCESS && rsp.GetApplied():
			return nil
		case rsp.GetRetInfo().GetCode() == pb.ErrorCode_SUCCESS:
			continue
		case rsp.GetRetInfo().GetCode() != pb.ErrorCode_VIEW_NOT_READY:
			return fmt.Errorf("等待 View 跟上导入的写入：%s", rsp.GetRetInfo().GetMsg())
		}
		timer := time.NewTimer(importViewRetryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// importUpserts 把一批行转成 Storage 的字段更新（归档目标写的也是这一种行）；多标的文件每行的标的取 Batch.Subjects。空值写成显式 NULL（清除该字段，与因子机本地「整段替换」的结果一致，
// 否则 Storage 保留旧值、本地为 NULL，两边不一致，之后同步还会把本地改回旧值）。
func importUpserts(c runner.Chunk, types map[string]pb.FieldValueType) ([]*pb.RowFieldUpsert, error) {
	rows := make([]*pb.RowFieldUpsert, 0, c.Batch.Len())
	for i := 0; i < c.Batch.Len(); i++ {
		var values []*pb.FieldValue
		for fi, field := range c.Fields {
			col := c.Batch.Cols[fi]
			if col.Null[i] {
				values = append(values, &pb.FieldValue{FieldId: field.Name, Value: &pb.TypedValue{Value: &pb.TypedValue_NullValue{NullValue: pb.NullValue_NULL_VALUE_NULL}}})
				continue
			}
			value, err := importTypedValue(field, col, i, types[field.Name])
			if err != nil {
				return nil, fmt.Errorf("第 %d 行字段 %s：%w", i+1, field.Name, err)
			}
			values = append(values, &pb.FieldValue{FieldId: field.Name, Value: value})
		}
		subject := c.Subject
		if c.Batch.Subjects != nil {
			subject = c.Batch.Subjects[i]
		}
		rows = append(rows, &pb.RowFieldUpsert{
			Key: &pb.RowKey{SpaceId: c.Space, DatasetId: c.Dataset, Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
				SubjectId: subject, Freq: c.Freq, DataTime: c.Batch.Times[i].UTC().Format(time.RFC3339Nano), SeriesTag: c.Batch.Tags[i],
			}}},
			Fields: values,
		})
	}
	return rows, nil
}

func importTypedValue(field seriesformat.Field, col seriesformat.Column, i int, valueType pb.FieldValueType) (*pb.TypedValue, error) {
	switch field.Type {
	case seriesformat.TypeDouble:
		return &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: col.F[i]}}, nil
	case seriesformat.TypeInt:
		return &pb.TypedValue{Value: &pb.TypedValue_IntValue{IntValue: col.I[i]}}, nil
	case seriesformat.TypeBool:
		return &pb.TypedValue{Value: &pb.TypedValue_BoolValue{BoolValue: col.I[i] != 0}}, nil
	case seriesformat.TypeTime:
		return &pb.TypedValue{Value: &pb.TypedValue_TimeValue{TimeValue: col.S[i]}}, nil
	case seriesformat.TypeJSON:
		if !json.Valid([]byte(col.S[i])) {
			return nil, errors.New("invalid json")
		}
		return &pb.TypedValue{Value: &pb.TypedValue_JsonValue{JsonValue: col.S[i]}}, nil
	case seriesformat.TypeBytes:
		return &pb.TypedValue{Value: &pb.TypedValue_BytesValue{BytesValue: []byte(col.S[i])}}, nil
	default:
		return &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: col.S[i]}}, nil
	}
}

// ---------------------------------------------------------------- Storage 元数据与写入（经操作员 SSH 网关）

// gatewayStorageImportMetadataClient 通过 tRPC 读取远端元数据。
type gatewayStorageImportMetadataClient struct {
	Gateway gatewayclient.Invoker
}

// gatewayStorageDataWriter 经主机网关把数据写入 Storage。
type gatewayStorageDataWriter struct {
	Gateway gatewayclient.Invoker
	SpaceID string
}

func (c gatewayStorageImportMetadataClient) GetDataset(ctx context.Context, spaceID string, datasetID string) (*pb.Dataset, error) {
	rsp := &pb.GetDatasetRsp{}
	if err := postStorage(ctx, c.Gateway, metadataServiceName, "GetDataset", &pb.GetDatasetReq{SpaceId: spaceID, DatasetId: datasetID}, rsp); err != nil {
		return nil, err
	}
	return rsp.GetDataset(), nil
}

func (c gatewayStorageImportMetadataClient) GetView(ctx context.Context, spaceID, viewID string) (*pb.View, error) {
	rsp := &pb.GetViewRsp{}
	if err := postStorage(ctx, c.Gateway, metadataServiceName, "GetView", &pb.GetViewReq{SpaceId: spaceID, ViewId: viewID}, rsp); err != nil {
		return nil, err
	}
	return rsp.GetView(), nil
}

// checkImportView 确认 --view 指定的 View 读的就是导入的数据集：否则等它跟上变更日志也证明不了导入的行已可查。
func checkImportView(ctx context.Context, meta interface {
	GetView(context.Context, string, string) (*pb.View, error)
}, spaceID, viewID, datasetID string) error {
	view, err := meta.GetView(ctx, spaceID, viewID)
	if err != nil {
		return fmt.Errorf("读取 View %s：%w", viewID, err)
	}
	if view.GetDatasetId() != datasetID {
		return fmt.Errorf("View %s 读的是数据集 %q，不是导入的数据集 %q", viewID, view.GetDatasetId(), datasetID)
	}
	return nil
}

func (c gatewayStorageImportMetadataClient) ListDatasetColumns(ctx context.Context, spaceID string, datasetID string) ([]*pb.DatasetColumn, error) {
	rsp := &pb.ListDatasetColumnsRsp{}
	if err := postStorage(ctx, c.Gateway, metadataServiceName, "ListDatasetColumns", &pb.ListDatasetColumnsReq{
		SpaceId: spaceID, DatasetId: datasetID, Page: &pb.Page{Page: 1, Size: 10000},
	}, rsp); err != nil {
		return nil, err
	}
	return rsp.GetColumns(), nil
}

func (c gatewayStorageImportMetadataClient) ListDatasetSubjects(ctx context.Context, spaceID string, datasetID string, subjectID string) ([]*pb.DatasetSubject, error) {
	rsp := &pb.ListDatasetSubjectsRsp{}
	if err := postStorage(ctx, c.Gateway, metadataServiceName, "ListDatasetSubjects", &pb.ListDatasetSubjectsReq{
		SpaceId: spaceID, DatasetId: datasetID, SubjectId: subjectID, Page: &pb.Page{Page: 1, Size: 10000},
	}, rsp); err != nil {
		return nil, err
	}
	return rsp.GetDatasetSubjects(), nil
}

func (w gatewayStorageDataWriter) UpsertFields(ctx context.Context, req *pb.PrimaryUpsertFieldsReq) error {
	if req == nil || w.SpaceID == "" {
		return fmt.Errorf("Storage 导入需要请求与空间")
	}
	for _, row := range req.GetRows() {
		if row.GetKey().GetSpaceId() != w.SpaceID {
			return fmt.Errorf("Storage import row does not match the selected space")
		}
	}
	metadata := gatewayclient.CallMetadataFromContext(ctx)
	metadata.SpaceID = w.SpaceID
	ctx = gatewayclient.WithCallMetadata(ctx, metadata)
	return postStorage(ctx, w.Gateway, accessServiceName, "UpsertFields", req, &pb.PrimaryUpsertFieldsRsp{})
}

// WaitViewApplied 调 DataView，等 View 跟上调用这一刻的变更日志头部；业务结果在响应的 ret_info 里，由调用方判断。
func (w gatewayStorageDataWriter) WaitViewApplied(ctx context.Context, req *pb.WaitViewAppliedReq) (*pb.WaitViewAppliedRsp, error) {
	if req == nil {
		return nil, errors.New("等待 View 跟上写入需要请求")
	}
	rsp := &pb.WaitViewAppliedRsp{}
	if err := postStorageRaw(ctx, w.Gateway, dataViewSvc, "WaitViewApplied", req, rsp); err != nil {
		return nil, err
	}
	return rsp, nil
}

// storageImportColumnMap 返回数据集的有效列（按列名）。
func storageImportColumnMap(columns []*pb.DatasetColumn) (map[string]*pb.DatasetColumn, error) {
	values := make(map[string]*pb.DatasetColumn, len(columns))
	for _, column := range columns {
		if column.GetColumnName() == "" || column.GetStatus() == "deleted" {
			continue
		}
		values[column.GetColumnName()] = column
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("dataset columns are empty")
	}
	return values, nil
}

// storageImportSubjectBound 判断标的是否已经在数据集的标签范围内。
func storageImportSubjectBound(subjects []*pb.DatasetSubject, subjectID string) bool {
	for _, item := range subjects {
		if item.GetSubjectId() == subjectID && item.GetStatus() != "deleted" {
			return true
		}
	}
	return false
}
