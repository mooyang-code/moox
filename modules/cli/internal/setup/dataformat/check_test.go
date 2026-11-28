package dataformat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	adminschema "github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/deployscan"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	pkgdataformat "github.com/mooyang-code/moox/packages/dataformat"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/sqlitedb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
)

// 确认 servicecatalog 中每个组件都明确归为“12 个有状态组件”或“4 个无状态组件”，不会遗漏新组件。
func TestCatalogCoverageMatchesStatefulAndStatelessPartition(t *testing.T) {
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	stateless := map[string]bool{
		"host-gateway":     true,
		"host-agent":       true,
		"egress-proxy":     true,
		"collector-stream": true,
	}
	expected, err := Expected()
	require.NoError(t, err)
	require.Len(t, expected, 12)

	for _, comp := range catalog.Components {
		_, isStateful := expected[comp.ID]
		isStateless := stateless[comp.ID]
		require.True(t, isStateful != isStateless, "组件 %s 必须且只能归入有状态或无状态之一", comp.ID)
	}
}

// 验收 1：改一个模块的 schema 语义后，预检能列出它并拒绝保留；带着旧库启动该模块会拒绝并给出明确提示。
func TestAcceptanceSchemaSemanticChangeRejectedByPreflightAndStartup(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "admin.db")

	// 用当前 schema 初始化真实 admin 库并写出 data-format。
	db, err := sqlitedb.OpenGORM(sqlitedb.Options{Path: dbPath, Synchronous: "FULL", BusyTimeoutMS: 10000})
	require.NoError(t, err)
	require.NoError(t, sqlitedb.EnsureSchemaGORM(db, dbPath, "admin", adminschema.AdminSQL(), nil))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	rawFormat, err := os.ReadFile(filepath.Join(dir, pkgdataformat.FileName))
	require.NoError(t, err)

	// 制造语义变更：在 admin schema 里改写列默认值。
	in, err := DefaultInputs()
	require.NoError(t, err)
	mutatedSQL := strings.Replace(in.AdminSQL, "c_status TEXT NOT NULL DEFAULT 'active'", "c_status TEXT NOT NULL DEFAULT 'enabled'", 1)
	if mutatedSQL == in.AdminSQL {
		// 若表结构不含该字面量，则给 t_admin_users 增加一列，确保确实发生语义变化。
		mutatedSQL = in.AdminSQL + "\nCREATE TABLE IF NOT EXISTS t_admin_format_probe (c_id INTEGER PRIMARY KEY);\n"
	}
	in.AdminSQL = mutatedSQL
	expected, err := ExpectedWith(in)
	require.NoError(t, err)

	report := CheckHost("control", []string{"host-gateway", "admin", "monitor"}, map[string]deployscan.ComponentDataFormat{
		"admin": {HasData: true, FormatText: string(rawFormat)},
	}, expected)
	require.False(t, report.CanKeep)
	require.Len(t, report.Incompatible, 1)
	require.Equal(t, "admin", report.Incompatible[0].Component)
	require.Equal(t, StatusChanged, report.Incompatible[0].Status)
	require.Equal(t, []string{pkgdataformat.PartSQLiteSchema}, report.Incompatible[0].ChangedParts)

	err = IncompatibleError(report)
	require.Error(t, err)
	require.ErrorContains(t, err, "data_format_incompatible")
	require.ErrorContains(t, err, "control/admin")
	require.ErrorContains(t, err, "全部覆盖")

	// 带着旧库用新 schema 启动：必须拒绝并给出明确提示。
	reopened, err := sqlitedb.OpenGORM(sqlitedb.Options{Path: dbPath})
	require.NoError(t, err)
	defer func() {
		if raw, e := reopened.DB(); e == nil {
			_ = raw.Close()
		}
	}()
	startErr := sqlitedb.EnsureSchemaGORM(reopened, dbPath, "admin", mutatedSQL, nil)
	require.ErrorIs(t, startErr, sqlitedb.ErrIncompatibleSchema)
	require.ErrorContains(t, startErr, "全新部署")
}

// 验收 2：只改 schema 的注释、缩进与换行，预检不列它，启动也不拒绝。
func TestAcceptanceSchemaFormattingOnlyChangeAllowed(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "admin.db")

	db, err := sqlitedb.OpenGORM(sqlitedb.Options{Path: dbPath})
	require.NoError(t, err)
	require.NoError(t, sqlitedb.EnsureSchemaGORM(db, dbPath, "admin", adminschema.AdminSQL(), nil))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	rawFormat, err := os.ReadFile(filepath.Join(dir, pkgdataformat.FileName))
	require.NoError(t, err)

	in, err := DefaultInputs()
	require.NoError(t, err)
	reformatted := strings.NewReplacer(
		"    ", "\t",
		", ", ",",
		"CREATE TABLE IF NOT EXISTS", "-- 仅排版与注释调整\ncreate table if not exists",
		"PRIMARY KEY (", "primary key(",
	).Replace(in.AdminSQL)
	require.NotEqual(t, in.AdminSQL, reformatted)
	in.AdminSQL = reformatted

	expected, err := ExpectedWith(in)
	require.NoError(t, err)

	report := CheckHost("control", []string{"admin"}, map[string]deployscan.ComponentDataFormat{
		"admin": {HasData: true, FormatText: string(rawFormat)},
	}, expected)
	require.True(t, report.CanKeep)
	require.Empty(t, report.Incompatible)
	require.Len(t, report.Checks, 1)
	require.Equal(t, StatusMatch, report.Checks[0].Status)
	require.NoError(t, IncompatibleError(report))

	// 启动校验同样放行。
	reopened, err := sqlitedb.OpenGORM(sqlitedb.Options{Path: dbPath})
	require.NoError(t, err)
	defer func() {
		if raw, e := reopened.DB(); e == nil {
			_ = raw.Close()
		}
	}()
	require.NoError(t, sqlitedb.EnsureSchemaGORM(reopened, dbPath, "admin", reformatted, nil))
}

// 验收 3：改一个事件负载字段，EventBus 与 storage-primary 被列出；改 lifecycle.json 版本常量，用到它的组件都被列出。
func TestAcceptanceEventPayloadAndLifecycleVersionSensitivity(t *testing.T) {
	baseExpected, err := Expected()
	require.NoError(t, err)

	// 模拟全部 12 个有状态组件均已有本地数据且落盘当前版本的 data-format。
	allObserved := make(map[string]deployscan.ComponentDataFormat, len(statefulComponents))
	for cid, rec := range baseExpected {
		raw, marshalErr := json.Marshal(rec)
		require.NoError(t, marshalErr)
		allObserved[cid] = deployscan.ComponentDataFormat{
			HasData:    true,
			FormatText: string(raw),
		}
	}

	// 3a：给事件负载消息增加一个字段，用真实 proto descriptor 重算事件摘要。
	mutatedEventsDigest := buildMutatedEventsDigest(t)
	eventInputs, err := DefaultInputs()
	require.NoError(t, err)
	require.NotEqual(t, eventInputs.EventsDigest, mutatedEventsDigest)
	eventInputs.EventsDigest = mutatedEventsDigest
	eventExpected, err := ExpectedWith(eventInputs)
	require.NoError(t, err)

	eventReport := CheckHost("all", statefulComponents, allObserved, eventExpected)
	require.False(t, eventReport.CanKeep)
	gotEventChanged := make([]string, 0, len(eventReport.Incompatible))
	for _, item := range eventReport.Incompatible {
		gotEventChanged = append(gotEventChanged, item.Component)
		require.Equal(t, StatusChanged, item.Status)
		require.Equal(t, []string{pkgdataformat.PartEvents}, item.ChangedParts)
	}
	slices.Sort(gotEventChanged)
	require.Equal(t, []string{"eventbus", "storage-primary"}, gotEventChanged)

	// 3a-2：只改 Stream 的容量与保留期运行参数，不触发格式变化，允许保留数据。
	tunedStreams := events.Streams()
	tunedStreams[0].MaxAge *= 2
	tunedStreams[0].MaxBytes *= 2
	tunedInputs, err := DefaultInputs()
	require.NoError(t, err)
	tunedInputs.EventBusTopologyDigest = events.TopologyFormatDigestFromSpecs(tunedStreams, events.Consumers())
	require.Equal(t, baseExpected["eventbus"].Parts[pkgdataformat.PartEventBusTopology], tunedInputs.EventBusTopologyDigest)
	tunedExpected, err := ExpectedWith(tunedInputs)
	require.NoError(t, err)
	tunedReport := CheckHost("all", statefulComponents, allObserved, tunedExpected)
	require.True(t, tunedReport.CanKeep)
	require.Empty(t, tunedReport.Incompatible)

	// 3a-3：改 Stream 主题等影响磁盘读回的拓扑结构，只有 EventBus 被判格式变化，storage-primary 不受影响。
	streams := events.Streams()
	streams[0].Subjects = append(streams[0].Subjects, "moox.probe.v1.>")
	topoInputs, err := DefaultInputs()
	require.NoError(t, err)
	topoInputs.EventBusTopologyDigest = events.TopologyFormatDigestFromSpecs(streams, events.Consumers())
	topoExpected, err := ExpectedWith(topoInputs)
	require.NoError(t, err)
	topoReport := CheckHost("all", statefulComponents, allObserved, topoExpected)
	require.False(t, topoReport.CanKeep)
	require.Len(t, topoReport.Incompatible, 1)
	require.Equal(t, "eventbus", topoReport.Incompatible[0].Component)
	require.Equal(t, []string{pkgdataformat.PartEventBusTopology}, topoReport.Incompatible[0].ChangedParts)

	// 3b：升级 LifecycleStateVersion，所有用到 lifecycle 的 5 个组件全部被列出，其余 7 个组件不受影响。
	lifeInputs, err := DefaultInputs()
	require.NoError(t, err)
	lifeInputs.LifecycleStateVersion++
	lifeExpected, err := ExpectedWith(lifeInputs)
	require.NoError(t, err)

	lifeReport := CheckHost("all", statefulComponents, allObserved, lifeExpected)
	require.False(t, lifeReport.CanKeep)
	gotLifeChanged := make([]string, 0, len(lifeReport.Incompatible))
	for _, item := range lifeReport.Incompatible {
		gotLifeChanged = append(gotLifeChanged, item.Component)
		require.Equal(t, StatusChanged, item.Status)
		require.Equal(t, []string{pkgdataformat.PartLifecycle}, item.ChangedParts)
	}
	slices.Sort(gotLifeChanged)
	require.Equal(t, []string{
		"collector",
		"factor-engine",
		"monitor",
		"strategy",
		"trade",
	}, gotLifeChanged)
}

// 验收 4：首次安装在控制机装完后中断，再选保留，预检对还没部署的组件判“尚无数据”放行，对 Admin 判“格式相符”放行。
func TestAcceptanceInterruptedFirstInstallAllowsKeep(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("本机没有 python3，跳过真实 inspect.py 验收")
	}

	// 模拟两台主机各自的本地临时部署根：control 已装完 Admin 并建好了空数据目录，storage 尚未部署。
	controlBase := t.TempDir()
	storageBase := t.TempDir()
	controlRoot := filepath.Join(controlBase, "moox")
	storageRoot := filepath.Join(storageBase, "moox")

	// 控制机：install.sh prepare 会为本机全部组件先建出空目录；随后离线/启动初始化了 admin.db 并写出 data-format。
	for _, dir := range []string{"data/admin", "data/monitor", "data/collector", "data/host-gateway", "data/host-agent"} {
		require.NoError(t, os.MkdirAll(filepath.Join(controlRoot, dir), 0o700))
	}
	adminDBPath := filepath.Join(controlRoot, "data/admin/admin.db")
	db, err := sqlitedb.OpenGORM(sqlitedb.Options{Path: adminDBPath, Synchronous: "FULL", BusyTimeoutMS: 10000})
	require.NoError(t, err)
	require.NoError(t, sqlitedb.EnsureSchemaGORM(db, adminDBPath, "admin", adminschema.AdminSQL(), nil))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	// 构造拓扑，用本地 python3 执行真实 deployscan.Scan（不同主机映射到各自的临时根目录）。
	cfg, err := topology.Parse([]byte(`
[paths]
deploy_root = "/data/moox"

[hosts.control]
address = "203.0.113.10"
ssh = { username = "ubuntu" }

[hosts.storage]
address = "203.0.113.11"
ssh = { username = "ubuntu" }

[deployments]
control = ["admin", "monitor", "collector"]
storage = ["storage-primary", "storage-view", "eventbus"]
`))
	require.NoError(t, err)

	hostRoots := map[string]string{
		"control": controlRoot,
		"storage": storageRoot, // 不存在的目录，模拟尚未部署的主机
	}
	dial := func(_ context.Context, host topology.Host) (setupssh.Client, error) {
		return localInspectSSH{python: python, hostRoot: hostRoots[host.Name], declaredRoot: cfg.Paths.DeployRoot, home: t.TempDir()}, nil
	}

	scanReports, err := deployscan.Scan(context.Background(), &cfg, dial, deployscan.Options{})
	require.NoError(t, err)
	require.Len(t, scanReports, 2)

	formatReport, err := CheckTopologyReports(&cfg, scanReports)
	require.NoError(t, err)
	require.True(t, formatReport.CanKeep, "首次安装中断后再选保留应放行：%+v", formatReport)
	require.NoError(t, IncompatibleError(formatReport))

	byKey := make(map[string]ComponentCheck, len(formatReport.Checks))
	for _, check := range formatReport.Checks {
		byKey[check.Host+"/"+check.Component] = check
	}
	require.Equal(t, StatusMatch, byKey["control/admin"].Status)
	for _, key := range []string{
		"control/monitor",
		"control/collector",
		"storage/storage-primary",
		"storage/storage-view",
		"storage/eventbus",
	} {
		require.Equal(t, StatusNoData, byKey[key].Status, "尚未启动的组件 %s 应判为尚无数据", key)
	}
	// 无状态组件不出现在比对列表里。
	_, hasGateway := byKey["control/host-gateway"]
	require.False(t, hasGateway)
}

func TestCheckHostRejectsUnknownAndMismatchedComponentFormat(t *testing.T) {
	expected, err := Expected()
	require.NoError(t, err)

	wrongRec, err := pkgdataformat.Build("monitor", map[string]string{pkgdataformat.PartSQLiteSchema: "sha256:abc"})
	require.NoError(t, err)
	wrongRaw, err := json.Marshal(wrongRec)
	require.NoError(t, err)

	report := CheckHost("control", []string{"admin", "monitor", "collector"}, map[string]deployscan.ComponentDataFormat{
		"admin":     {HasData: true, FormatText: ""},
		"monitor":   {HasData: true, FormatText: "{broken"},
		"collector": {HasData: true, FormatText: string(wrongRaw)},
	}, expected)
	require.False(t, report.CanKeep)
	require.Len(t, report.Incompatible, 3)
	for _, item := range report.Incompatible {
		require.Equal(t, StatusUnknown, item.Status)
	}
	require.ErrorContains(t, IncompatibleError(report), "格式未知")
}

func buildMutatedEventsDigest(t *testing.T) string {
	t.Helper()
	reg, err := events.DefaultRegistry()
	require.NoError(t, err)
	envelope := (&eventpb.EventMessage{}).ProtoReflect().Descriptor()

	origDesc := events.DatasetPeriodCompleted.NewPayload().ProtoReflect().Descriptor()
	origFile := protodesc.ToFileDescriptorProto(origDesc.ParentFile())
	withExtraField := proto.Clone(origFile).(*descriptorpb.FileDescriptorProto)
	mutated := false
	for _, msg := range withExtraField.MessageType {
		if msg.GetName() == string(origDesc.Name()) {
			msg.Field = append(msg.Field, &descriptorpb.FieldDescriptorProto{
				Name:     proto.String("extra_probe_field"),
				Number:   proto.Int32(99),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				JsonName: proto.String("extraProbeField"),
			})
			mutated = true
			break
		}
	}
	require.True(t, mutated)

	fileDesc, err := protodesc.NewFile(withExtraField, nil)
	require.NoError(t, err)
	modDesc := fileDesc.Messages().ByName(origDesc.Name())
	require.NotNil(t, modDesc)

	registered := reg.Events()
	specs := make([]events.EventDescriptorSpec, 0, len(registered))
	for _, ev := range registered {
		desc := ev.NewPayload().ProtoReflect().Descriptor()
		if ev.Name() == events.DatasetPeriodCompleted.Name() {
			desc = modDesc
		}
		specs = append(specs, events.EventDescriptorSpec{
			Name:       ev.Name(),
			Version:    ev.Version(),
			Stream:     ev.Stream(),
			Descriptor: desc,
		})
	}
	digest, err := events.MessagesFormatDigestFromSpecs(envelope, specs)
	require.NoError(t, err)
	return digest
}

// localInspectSSH 在本机用 python3 运行 inspect.py，把声明的部署根映射到本地临时目录。
type localInspectSSH struct {
	python       string
	hostRoot     string
	declaredRoot string
	home         string
}

func (l localInspectSSH) Check(context.Context) error { return nil }
func (l localInspectSSH) ForwardLocal(context.Context, string) (net.Listener, error) {
	return nil, errors.New("not used")
}
func (l localInspectSSH) Upload(context.Context, io.Reader, int64, string, fs.FileMode) error {
	return nil
}
func (l localInspectSSH) Download(context.Context, string, io.Writer) (int64, error) { return 0, nil }
func (l localInspectSSH) Close() error                                               { return nil }
func (l localInspectSSH) Run(ctx context.Context, argv []string, _ io.Reader) (setupssh.Result, error) {
	var req map[string]any
	if err := json.Unmarshal([]byte(argv[3]), &req); err != nil {
		return setupssh.Result{}, err
	}
	req["root"] = l.hostRoot
	rewritten, err := json.Marshal(req)
	if err != nil {
		return setupssh.Result{}, err
	}
	cmd := exec.CommandContext(ctx, l.python, "-I", "-c", argv[2], string(rewritten))
	cmd.Env = []string{"HOME=" + l.home, "PATH=/usr/bin:/bin"}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return setupssh.Result{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: 1}, err
	}
	// 把输出里的 root 改回 topology 声明的 deploy_root，符合 deployscan.inspect 的校验约定。
	var out map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return setupssh.Result{}, err
	}
	out["root"] = l.declaredRoot
	finalRaw, err := json.Marshal(out)
	if err != nil {
		return setupssh.Result{}, err
	}
	return setupssh.Result{Stdout: string(finalRaw)}, nil
}
