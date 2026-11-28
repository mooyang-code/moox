package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/mooyang-code/moox/modules/admin/internal/pki"
	"github.com/mooyang-code/moox/modules/admin/internal/service/keys"
	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayauth/authtest"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

func newBootstrapFixture(t *testing.T) bootstrapOptions {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	require.NoError(t, os.Mkdir(dir, 0o700))
	opts := bootstrapOptions{topologyFile: filepath.Join(dir, "topology.json"), dbPath: filepath.Join(dir, "data", "admin.db"), masterFile: filepath.Join(dir, "secrets", "admin-encryption.key"), pkiDir: filepath.Join(dir, "secrets", "pki"), outputDir: filepath.Join(dir, "bundles"),
		operatorPublicKey: identityPublicKey("operator")}
	writeBootstrapTopology(t, opts, bootstrapFixtureTopology())
	return opts
}

func bootstrapFixtureTopology() *pb.SyncTopologyReq {
	return &pb.SyncTopologyReq{Hosts: []*pb.SyncTopologyHost{
		{HostId: "control", Enabled: true, Address: "192.0.2.1", PrivateAddress: "control.internal.example.test", ComponentIds: []string{"admin", "collector"}},
		{HostId: "storage", Enabled: true, Address: "192.0.2.2", ComponentIds: []string{"storage-primary", "storage-view"}},
		{HostId: "overseas", Enabled: true, Address: "192.0.2.3", ComponentIds: []string{"egress-proxy", "trade"}},
	}}
}

func writeBootstrapTopology(t *testing.T, opts bootstrapOptions, topology *pb.SyncTopologyReq) {
	t.Helper()
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(topology)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(opts.topologyFile, raw, 0o600))
}

func bootstrapArguments(opts bootstrapOptions) []string {
	args := []string{"bootstrap", "--topology-file", opts.topologyFile, "--db-path", opts.dbPath, "--encryption-key-file", opts.masterFile, "--pki-dir", opts.pkiDir, "--output-dir", opts.outputDir,
		"--operator-public-key", opts.operatorPublicKey}
	return append(args, currentKeyArguments(opts.current)...)
}

func currentKeyArguments(current currentKeys) []string {
	var args []string
	if current.host != "" {
		args = append(args, "--current-sign-public-key", current.host)
	}
	if current.scfCollector != "" {
		args = append(args, "--current-scf-collector-public-key", current.scfCollector)
	}
	return args
}

func runBootstrapFixture(t *testing.T, opts bootstrapOptions) hostbundle.Metadata {
	t.Helper()
	var out, stderr bytes.Buffer
	require.NoError(t, runBootstrapCommand(bootstrapArguments(opts), &out, &stderr), stderr.String())
	require.NotContains(t, out.String()+stderr.String(), "BEGIN")
	var result hostbundle.Metadata
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))
	return result
}

// bundleFiles 是身份包目录里的文件名（排序后）。
func bundleFiles(t *testing.T, metadata hostbundle.Metadata) []string {
	t.Helper()
	entries, err := os.ReadDir(metadata.BundleDir)
	require.NoError(t, err)
	names := []string{}
	for _, entry := range entries {
		require.False(t, entry.IsDir(), entry.Name())
		info, err := entry.Info()
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), entry.Name())
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	return names
}

// bundleKeyPublic 读身份包里一把签名私钥对应的公钥，并确认清单与输出里都没有私钥内容。
func bundleKeyPublic(t *testing.T, metadata hostbundle.Metadata, name string) string {
	t.Helper()
	key, err := gatewayauth.ReadSigningKey(filepath.Join(metadata.BundleDir, name))
	require.NoError(t, err)
	manifest, err := os.ReadFile(filepath.Join(metadata.BundleDir, "bundle.json"))
	require.NoError(t, err)
	require.NotContains(t, string(manifest), gatewayauth.EncodeSigningKey(key))
	return gatewayauth.EncodePublicKey(gatewayauth.Credentials{Key: key}.PublicKey())
}

func registeredKeys(t *testing.T, opts bootstrapOptions) (hosts map[string]string, identities map[string]string) {
	t.Helper()
	db, err := openAdminCLIDB(opts.dbPath)
	require.NoError(t, err)
	defer closeAdminCLIDB(db)
	store, err := keys.NewStore(db)
	require.NoError(t, err)
	hosts, identities = map[string]string{}, map[string]string{}
	hostRows, err := store.Hosts(context.Background())
	require.NoError(t, err)
	for _, row := range hostRows {
		hosts[row.HostID] = row.PublicKey
	}
	identityRows, err := store.Identities(context.Background())
	require.NoError(t, err)
	for _, row := range identityRows {
		identities[row.Identity] = row.PublicKey
	}
	return hosts, identities
}

// 空环境：建库、CA、主密钥，登记全部主机；控制机的身份包里有主机签名私钥、主机证书、根证书与 scf-collector 的私钥，
// 公钥登记在 Admin；操作电脑的 moox-cli 只登记公钥，包里没有它的私钥。
func TestBootstrapEmptyEnvironmentIssuesHostKeyAndRegistersOperator(t *testing.T) {
	opts := newBootstrapFixture(t)
	result := runBootstrapFixture(t, opts)
	require.True(t, result.CA.Created)
	require.Equal(t, 2, result.Version)
	require.True(t, result.SignKeyIssued)
	require.True(t, result.SCFCollectorKeyIssued)
	require.Equal(t, opts.operatorPublicKey, result.OperatorPublicKey)
	require.Equal(t, []string{"bundle.json", "host.crt", "host.key", "host.tls.key", "moox-ca.crt", "scf-collector.key"}, bundleFiles(t, result))
	require.Equal(t, result.SignPublicKey, bundleKeyPublic(t, result, hostbundle.HostKeyFile))
	require.Equal(t, result.SCFCollectorPublicKey, bundleKeyPublic(t, result, hostbundle.SCFCollectorKeyFile))
	require.NotEqual(t, result.SignPublicKey, result.SCFCollectorPublicKey)

	hosts, identities := registeredKeys(t, opts)
	require.Equal(t, map[string]string{"control": result.SignPublicKey, "storage": "", "overseas": ""}, hosts, "别的主机到自己签发身份包时才有公钥")
	require.Equal(t, map[string]string{"moox-cli": opts.operatorPublicKey, "scf-collector": result.SCFCollectorPublicKey}, identities)

	// 主机证书：由 MooX CA 签给控制机的地址。
	pair, err := tls.LoadX509KeyPair(filepath.Join(result.BundleDir, hostbundle.HostCertificateFile), filepath.Join(result.BundleDir, hostbundle.HostTLSKeyFile))
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	require.NoError(t, err)
	roots := x509.NewCertPool()
	ca, err := os.ReadFile(filepath.Join(result.BundleDir, hostbundle.CAFile))
	require.NoError(t, err)
	require.True(t, roots.AppendCertsFromPEM(ca))
	for _, name := range []string{"control", "192.0.2.1", "control.internal.example.test"} {
		_, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: name})
		require.NoError(t, err, name)
	}
	// Admin 主密钥与 CA 私钥不进包。
	for _, name := range []string{opts.masterFile, filepath.Join(opts.pkiDir, "ca.key")} {
		secret, err := os.ReadFile(name)
		require.NoError(t, err)
		for _, file := range bundleFiles(t, result) {
			raw, err := os.ReadFile(filepath.Join(result.BundleDir, file))
			require.NoError(t, err)
			require.NotContains(t, string(raw), strings.TrimSpace(string(secret)), file)
		}
	}
	// 快照里是公钥：控制机的主机密钥，加两个非主机身份；网关按快照里的部署算出控制机上可签名的调用方。
	db, err := openAdminCLIDB(opts.dbPath)
	require.NoError(t, err)
	defer closeAdminCLIDB(db)
	dao, err := sysdeploy.NewTopologyDAO(db, "control")
	require.NoError(t, err)
	snapshot, err := dao.Snapshot(context.Background())
	require.NoError(t, err)
	require.Len(t, snapshot.Identities, 2)
	index := slices.IndexFunc(snapshot.Hosts, func(host *pb.SnapshotHost) bool { return host.HostId == "control" })
	require.GreaterOrEqual(t, index, 0)
	require.Equal(t, result.SignPublicKey, gatewayauth.EncodePublicKey(snapshot.Hosts[index].SignPublicKey))
	for _, host := range snapshot.Hosts {
		require.Equal(t, host.HostId == "control", len(host.SignPublicKey) != 0, "只有控制机签发过身份包：%s", host.HostId)
	}
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	status := map[bool]string{true: servicecatalog.Enabled, false: servicecatalog.Disabled}
	var snapshotHosts []servicecatalog.Host
	for _, host := range snapshot.Hosts {
		snapshotHosts = append(snapshotHosts, servicecatalog.Host{ID: host.HostId, Address: host.Address, Status: status[host.Enabled]})
	}
	var snapshotDeployments []servicecatalog.Deployment
	for _, deployment := range snapshot.Deployments {
		snapshotDeployments = append(snapshotDeployments, servicecatalog.Deployment{HostID: deployment.HostId, ComponentID: deployment.ComponentId, Status: status[deployment.Enabled]})
	}
	require.Equal(t, []string{"admin", "collector", "console", "host-agent", "host-gateway"}, catalog.SigningCallers(servicecatalog.NewTopology(snapshotHosts, snapshotDeployments))["control"])
}

// 重跑（例行升级、续证书）：目标机上的私钥和登记的一致就不换密钥，包里没有私钥；主密钥、CA、已登记的操作电脑公钥都不变；
// 启停状态取自拓扑文件（moox.toml 的声明）。
func TestBootstrapRetryKeepsKeysMasterCAAndTakesStatesFromTopology(t *testing.T) {
	opts := newBootstrapFixture(t)
	first := runBootstrapFixture(t, opts)
	master, err := os.ReadFile(opts.masterFile)
	require.NoError(t, err)
	declared := bootstrapFixtureTopology()
	declared.Hosts[1].Enabled = false
	declared.Hosts[2].DisabledComponentIds = []string{"trade"}
	writeBootstrapTopology(t, opts, declared)

	opts.current = currentKeys{host: first.SignPublicKey, scfCollector: first.SCFCollectorPublicKey}
	// 另一台电脑的公钥：bootstrap 不改已经登记的那一把（换电脑走 setup operator --register）。
	opts.operatorPublicKey = identityPublicKey("another-computer")
	second := runBootstrapFixture(t, opts)
	require.False(t, second.CA.Created)
	require.Equal(t, first.CA.CertificateInfo, second.CA.CertificateInfo)
	require.False(t, second.SignKeyIssued)
	require.False(t, second.SCFCollectorKeyIssued)
	require.Equal(t, first.SignPublicKey, second.SignPublicKey)
	require.Equal(t, first.SCFCollectorPublicKey, second.SCFCollectorPublicKey)
	require.Equal(t, first.OperatorPublicKey, second.OperatorPublicKey)
	require.Equal(t, []string{"bundle.json", "host.crt", "host.tls.key", "moox-ca.crt"}, bundleFiles(t, second))
	require.NotEqual(t, first.Certificate.Serial, second.Certificate.Serial, "证书每次重新签发")
	again, err := os.ReadFile(opts.masterFile)
	require.NoError(t, err)
	require.Equal(t, master, again)
	db, err := openAdminCLIDB(opts.dbPath)
	require.NoError(t, err)
	defer closeAdminCLIDB(db)
	dao, err := sysdeploy.NewTopologyDAO(db, "control")
	require.NoError(t, err)
	hosts, deployments, err := dao.Read(context.Background())
	require.NoError(t, err)
	require.Equal(t, "disabled", hosts[slices.IndexFunc(hosts, func(h sysdeploy.HostRecord) bool { return h.HostID == "storage" })].Status)
	require.Equal(t, "disabled", deployments[slices.IndexFunc(deployments, func(p sysdeploy.DeploymentRecord) bool { return p.HostID == "overseas" && p.ComponentID == "trade" })].Status)
}

// 目标机上没有私钥（或和登记的对不上）：新生成一把并登记，旧公钥随即不在了，没有新旧并存。
func TestBootstrapReissuesWhenTargetKeyIsMissingOrDifferent(t *testing.T) {
	opts := newBootstrapFixture(t)
	first := runBootstrapFixture(t, opts)
	for _, current := range []currentKeys{{}, {host: identityPublicKey("stale"), scfCollector: identityPublicKey("stale-scf")}} {
		opts.current = current
		next := runBootstrapFixture(t, opts)
		require.True(t, next.SignKeyIssued)
		require.True(t, next.SCFCollectorKeyIssued)
		require.NotEqual(t, first.SignPublicKey, next.SignPublicKey)
		require.Equal(t, next.SignPublicKey, bundleKeyPublic(t, next, hostbundle.HostKeyFile))
		hosts, identities := registeredKeys(t, opts)
		require.Equal(t, next.SignPublicKey, hosts["control"])
		require.Equal(t, next.SCFCollectorPublicKey, identities["scf-collector"])
		first = next
	}
	// 只丢了 scf-collector 的私钥：主机密钥不动。
	opts.current = currentKeys{host: first.SignPublicKey}
	next := runBootstrapFixture(t, opts)
	require.False(t, next.SignKeyIssued)
	require.True(t, next.SCFCollectorKeyIssued)
	require.Equal(t, []string{"bundle.json", "host.crt", "host.tls.key", "moox-ca.crt", "scf-collector.key"}, bundleFiles(t, next))
}

func TestBootstrapRejectsInvalidTopologyBeforeCreatingTrustMaterial(t *testing.T) {
	for _, mutate := range []func(*pb.SyncTopologyReq){
		func(req *pb.SyncTopologyReq) {
			req.Hosts[1].ComponentIds = append(req.Hosts[1].ComponentIds, "collector")
		},
		func(req *pb.SyncTopologyReq) { req.Hosts[0].ComponentIds = []string{"collector"} },
		func(req *pb.SyncTopologyReq) { req.Hosts[1].HostId = req.Hosts[0].HostId },
		func(req *pb.SyncTopologyReq) { req.Hosts[1].Address = "http://invalid" },
		func(req *pb.SyncTopologyReq) { req.Hosts[1].ComponentIds = []string{"host-gateway"} },
	} {
		opts := newBootstrapFixture(t)
		topology, _, err := readBootstrapTopology(opts.topologyFile)
		require.NoError(t, err)
		mutate(topology)
		writeBootstrapTopology(t, opts, topology)
		var out bytes.Buffer
		require.Error(t, runBootstrapCommand(bootstrapArguments(opts), &out, nil))
		require.Empty(t, out.String())
		for _, path := range []string{opts.masterFile, opts.pkiDir, opts.outputDir} {
			_, err := os.Lstat(path)
			require.True(t, os.IsNotExist(err), path)
		}
		if _, err := os.Lstat(opts.dbPath); err == nil {
			hosts, identities := registeredKeys(t, opts)
			require.Empty(t, hosts)
			require.Empty(t, identities)
		}
	}
}

// 库里已经登记过签名公钥、CA 目录却没了：不悄悄换一个新的信任根，库里的内容也不动。
func TestBootstrapRejectsLostTrustRootAndRollsBack(t *testing.T) {
	opts := newBootstrapFixture(t)
	first := runBootstrapFixture(t, opts)
	require.NoError(t, os.Rename(opts.pkiDir, opts.pkiDir+".lost"))
	opts.current = currentKeys{}
	err := runBootstrapCommand(bootstrapArguments(opts), nil, nil)
	require.ErrorIs(t, err, pki.ErrInvalidCA)
	hosts, _ := registeredKeys(t, opts)
	require.Equal(t, first.SignPublicKey, hosts["control"], "失败时不换掉已登记的公钥")
	require.NoError(t, os.Rename(opts.pkiDir+".lost", opts.pkiDir))
	opts.current = currentKeys{host: first.SignPublicKey, scfCollector: first.SCFCollectorPublicKey}
	require.False(t, runBootstrapFixture(t, opts).SignKeyIssued)
}

// 身份包没写出去（输出目录不可用）：这次生成的私钥就丢了。重跑时目标机上的公钥和登记的对不上，会再签发一把，能自己恢复。
func TestBootstrapBundleFailureRecoversOnRetry(t *testing.T) {
	opts := newBootstrapFixture(t)
	require.NoError(t, os.WriteFile(opts.outputDir, []byte("not a directory"), 0o600))
	require.Error(t, runBootstrapCommand(bootstrapArguments(opts), nil, nil))
	require.NoError(t, os.Remove(opts.outputDir))
	result := runBootstrapFixture(t, opts)
	require.True(t, result.SignKeyIssued)
	hosts, _ := registeredKeys(t, opts)
	require.Equal(t, result.SignPublicKey, hosts["control"])
	entries, err := os.ReadDir(opts.outputDir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "没有留下半个包")
}

// 几个进程同时 bootstrap：由文件锁排队，CA 只建一次；最后登记的主机公钥属于其中一个完整的身份包。
func TestBootstrapConcurrentProcessesShareOneTrustRoot(t *testing.T) {
	opts := newBootstrapFixture(t)
	executable, err := os.Executable()
	require.NoError(t, err)
	raw, err := json.Marshal(bootstrapArguments(opts))
	require.NoError(t, err)
	const writers = 4
	commands := make([]*exec.Cmd, writers)
	outputs := make([]bytes.Buffer, writers)
	errors := make([]error, writers)
	var wait sync.WaitGroup
	for i := range commands {
		commands[i] = exec.Command(executable, "-test.run=^TestBootstrapSubprocess$")
		commands[i].Env = append(os.Environ(), "MOOX_TEST_BOOTSTRAP_ARGS="+string(raw))
		commands[i].Stdout, commands[i].Stderr = &outputs[i], &outputs[i]
		wait.Go(func() { errors[i] = commands[i].Run() })
	}
	wait.Wait()
	created := 0
	var first hostbundle.Metadata
	issued := []string{}
	for i, err := range errors {
		require.NoError(t, err, outputs[i].String())
		var result hostbundle.Metadata
		require.NoError(t, json.Unmarshal(outputs[i].Bytes(), &result))
		if first.CA.SHA256 == "" {
			first = result
		}
		require.Equal(t, first.CA.CertificateInfo, result.CA.CertificateInfo)
		require.Equal(t, opts.operatorPublicKey, result.OperatorPublicKey)
		issued = append(issued, result.SignPublicKey)
		if result.CA.Created {
			created++
		}
	}
	require.Equal(t, 1, created)
	hosts, _ := registeredKeys(t, opts)
	require.Contains(t, issued, hosts["control"])
	files, err := os.ReadDir(opts.outputDir)
	require.NoError(t, err)
	require.Len(t, files, writers)
}

func TestBootstrapSubprocess(t *testing.T) {
	raw := os.Getenv("MOOX_TEST_BOOTSTRAP_ARGS")
	if raw == "" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		os.Exit(2)
	}
	if err := runBootstrapCommand(args, os.Stdout, os.Stderr); err != nil {
		printInitError(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestBootstrapRejectsFlagsAndStrictInputBeforeDatabaseCreation(t *testing.T) {
	for _, args := range [][]string{{}, {"bootstrap"}, {"bootstrap", "unexpected"}, {"bootstrap", "--unknown"}} {
		require.Error(t, runBootstrapCommand(args, nil, nil))
	}
	// 没有（或不是合法的）操作电脑公钥、现有公钥格式不对：在建库之前拒绝。
	for _, mutate := range []func(*bootstrapOptions){
		func(o *bootstrapOptions) { o.operatorPublicKey = "" },
		func(o *bootstrapOptions) { o.operatorPublicKey = "not-a-public-key" },
		func(o *bootstrapOptions) {
			o.operatorPublicKey = gatewayauth.EncodeSigningKey(authtest.Key("private")) + "x"
		},
		func(o *bootstrapOptions) { o.current.host = "bad" },
		func(o *bootstrapOptions) { o.current.scfCollector = "bad" },
	} {
		opts := newBootstrapFixture(t)
		mutate(&opts)
		args := bootstrapArguments(opts)
		if opts.operatorPublicKey == "" {
			args = slices.DeleteFunc(args, func(arg string) bool { return arg == "--operator-public-key" || arg == "" })
		}
		require.Error(t, runBootstrapCommand(args, nil, nil))
		_, err := os.Lstat(opts.dbPath)
		require.True(t, os.IsNotExist(err))
	}
	for _, raw := range []string{
		`{"hosts":[],"ssh_password":"fixture-do-not-echo"}`,
		`{"hosts":[{"host_id":"storage","enabled":true}]}`,
		`{"hosts":[]}`,
		`{} {}`,
		strings.Repeat(" ", (1<<20)+1),
	} {
		opts := newBootstrapFixture(t)
		require.NoError(t, os.WriteFile(opts.topologyFile, []byte(raw), 0o600))
		err := runBootstrapCommand(bootstrapArguments(opts), nil, nil)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "fixture-do-not-echo")
		_, err = os.Lstat(opts.dbPath)
		require.True(t, os.IsNotExist(err))
	}
	require.True(t, isBootstrapCommand([]string{"admin-cli", "bootstrap"}))
}
