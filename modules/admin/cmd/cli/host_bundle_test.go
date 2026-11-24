package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
	"github.com/stretchr/testify/require"
)

type hostBundleRun struct {
	hostID  string
	current currentKeys
	rotate  bool
}

func hostBundleArguments(opts bootstrapOptions, run hostBundleRun) []string {
	args := []string{"host-bundle", "--host-id", run.hostID, "--control-host-id", "control", "--db-path", opts.dbPath, "--encryption-key-file", opts.masterFile, "--pki-dir", opts.pkiDir, "--output-dir", opts.outputDir}
	args = append(args, currentKeyArguments(run.current)...)
	if run.rotate {
		args = append(args, "--rotate-sign-key")
	}
	return args
}

func runHostBundleFixture(t *testing.T, opts bootstrapOptions, run hostBundleRun) hostbundle.Metadata {
	t.Helper()
	var stdout, stderr bytes.Buffer
	require.NoError(t, runHostBundleCommand(hostBundleArguments(opts, run), &stdout, &stderr), stderr.String())
	var result hostbundle.Metadata
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &result))
	metadata, err := os.ReadFile(filepath.Join(result.BundleDir, "bundle.json"))
	require.NoError(t, err)
	var saved hostbundle.Metadata
	require.NoError(t, json.Unmarshal(metadata, &saved))
	require.Equal(t, result, saved)
	require.NotContains(t, stdout.String()+stderr.String(), "BEGIN")
	return result
}

// 每台主机一把签名密钥：第一次签发时生成并登记公钥；之后目标机上的私钥和登记的一致就沿用，证书照常重签；拓扑不被改动。
func TestHostBundleIssuesOneKeyPerHostAndReusesItOnRedeploy(t *testing.T) {
	opts := newBootstrapFixture(t)
	bootstrap := runBootstrapFixture(t, opts)
	declared := bootstrapFixtureTopology()
	declared.Hosts[1].Disabled = true
	declared.Hosts[2].DisabledComponents = []string{"egress-proxy"}
	writeBootstrapTopology(t, opts, declared)
	bootstrap = runBootstrapFixture(t, opts)
	db, err := openAdminCLIDB(opts.dbPath)
	require.NoError(t, err)
	dao, err := sysdeploy.NewTopologyDAO(db, "control")
	require.NoError(t, err)
	beforeHosts, beforePlacements, err := dao.Read(context.Background())
	require.NoError(t, err)
	closeAdminCLIDB(db)
	issued := map[string]string{"control": bootstrap.SignPublicKey}
	for _, hostID := range []string{"storage", "overseas"} {
		first := runHostBundleFixture(t, opts, hostBundleRun{hostID: hostID})
		require.Equal(t, hostID, first.HostID)
		require.Equal(t, "control", first.ControlHostID)
		require.False(t, first.CA.Created)
		require.Equal(t, bootstrap.CA.CertificateInfo, first.CA.CertificateInfo)
		require.True(t, first.SignKeyIssued)
		require.Empty(t, first.SCFCollectorPublicKey, "只有部署了 collector 的主机有 scf-collector 的私钥")
		require.Empty(t, first.OperatorPublicKey)
		require.Equal(t, []string{"bundle.json", "host.crt", "host.key", "host.tls.key", "moox-ca.crt"}, bundleFiles(t, first))
		require.Equal(t, first.SignPublicKey, bundleKeyPublic(t, first, hostbundle.HostKeyFile))
		require.NotContains(t, issued, first.SignPublicKey)
		issued[hostID] = first.SignPublicKey

		again := runHostBundleFixture(t, opts, hostBundleRun{hostID: hostID, current: currentKeys{host: first.SignPublicKey}})
		require.False(t, again.SignKeyIssued)
		require.Equal(t, first.SignPublicKey, again.SignPublicKey)
		require.Equal(t, []string{"bundle.json", "host.crt", "host.tls.key", "moox-ca.crt"}, bundleFiles(t, again))
		require.NotEqual(t, first.BundleDir, again.BundleDir)
		require.NotEqual(t, first.Certificate.Serial, again.Certificate.Serial)

		pair, err := tls.LoadX509KeyPair(filepath.Join(again.BundleDir, hostbundle.HostCertificateFile), filepath.Join(again.BundleDir, hostbundle.HostTLSKeyFile))
		require.NoError(t, err)
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		require.NoError(t, err)
		roots := x509.NewCertPool()
		ca, err := os.ReadFile(filepath.Join(again.BundleDir, hostbundle.CAFile))
		require.NoError(t, err)
		require.True(t, roots.AppendCertsFromPEM(ca))
		host := beforeHosts[slices.IndexFunc(beforeHosts, func(h sysdeploy.HostRecord) bool { return h.HostID == hostID })]
		for _, name := range []string{host.HostID, host.Address} {
			_, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: name})
			require.NoError(t, err)
		}
		require.Error(t, leaf.VerifyHostname("unrelated-host"))
	}
	hosts, identities := registeredKeys(t, opts)
	require.Equal(t, issued, hosts)
	require.Len(t, identities, 2, "签发别的主机的身份包不动非主机身份")
	db, err = openAdminCLIDB(opts.dbPath)
	require.NoError(t, err)
	defer closeAdminCLIDB(db)
	dao, err = sysdeploy.NewTopologyDAO(db, "control")
	require.NoError(t, err)
	afterHosts, afterPlacements, err := dao.Read(context.Background())
	require.NoError(t, err)
	require.Equal(t, beforePlacements, afterPlacements)
	for i := range beforeHosts {
		require.Equal(t, beforeHosts[i].Status, afterHosts[i].Status)
		require.Equal(t, beforeHosts[i].Address, afterHosts[i].Address)
	}
}

// 换密钥：显式要求，或目标机上的私钥丢了、和登记的对不上。新公钥直接替换旧的，别的主机的密钥不受影响。
func TestHostBundleRotatesOnRequestOrWhenTargetKeyDiffers(t *testing.T) {
	opts := newBootstrapFixture(t)
	bootstrap := runBootstrapFixture(t, opts)
	first := runHostBundleFixture(t, opts, hostBundleRun{hostID: "storage"})
	current := first.SignPublicKey
	for name, run := range map[string]hostBundleRun{
		"显式要求":       {hostID: "storage", current: currentKeys{host: current}, rotate: true},
		"目标机上没有私钥":   {hostID: "storage"},
		"目标机上是别的私钥":  {hostID: "storage", current: currentKeys{host: identityPublicKey("another")}},
		"拿别的主机的公钥来对": {hostID: "storage", current: currentKeys{host: bootstrap.SignPublicKey}},
	} {
		if run.rotate {
			run.current.host = current
		}
		next := runHostBundleFixture(t, opts, run)
		require.True(t, next.SignKeyIssued, name)
		require.NotEqual(t, current, next.SignPublicKey, name)
		require.Equal(t, next.SignPublicKey, bundleKeyPublic(t, next, hostbundle.HostKeyFile), name)
		hosts, _ := registeredKeys(t, opts)
		require.Equal(t, next.SignPublicKey, hosts["storage"], name)
		require.Equal(t, bootstrap.SignPublicKey, hosts["control"], name)
		require.NotContains(t, hosts, current)
		current = next.SignPublicKey
	}
}

// 控制机（部署了 collector）经 host-bundle 重新签发时，scf-collector 的私钥同样按“一致就沿用”处理。
func TestHostBundleForControlKeepsOrReissuesSCFCollectorKey(t *testing.T) {
	opts := newBootstrapFixture(t)
	bootstrap := runBootstrapFixture(t, opts)
	kept := runHostBundleFixture(t, opts, hostBundleRun{hostID: "control", current: currentKeys{host: bootstrap.SignPublicKey, scfCollector: bootstrap.SCFCollectorPublicKey}})
	require.False(t, kept.SignKeyIssued)
	require.False(t, kept.SCFCollectorKeyIssued)
	require.Equal(t, bootstrap.SCFCollectorPublicKey, kept.SCFCollectorPublicKey)
	reissued := runHostBundleFixture(t, opts, hostBundleRun{hostID: "control", current: currentKeys{host: bootstrap.SignPublicKey}})
	require.False(t, reissued.SignKeyIssued)
	require.True(t, reissued.SCFCollectorKeyIssued)
	require.Equal(t, reissued.SCFCollectorPublicKey, bundleKeyPublic(t, reissued, hostbundle.SCFCollectorKeyFile))
	_, identities := registeredKeys(t, opts)
	require.Equal(t, reissued.SCFCollectorPublicKey, identities["scf-collector"])
	require.Equal(t, opts.operatorPublicKey, identities["moox-cli"])
}

func TestHostBundleProvisionsNewlyRegisteredHostAndRejectsUnknownOnes(t *testing.T) {
	opts := newBootstrapFixture(t)
	runBootstrapFixture(t, opts)
	require.Error(t, runHostBundleCommand(hostBundleArguments(opts, hostBundleRun{hostID: "factor"}), nil, nil), "没登记的主机不签发")
	declared := bootstrapFixtureTopology()
	declared.Hosts = append(declared.Hosts, hostbundle.Host{HostID: "factor", Address: "192.168.0.103", Reachability: "outbound", Components: []string{"factor-engine"}})
	writeBootstrapTopology(t, opts, declared)
	runBootstrapFixture(t, opts)
	result := runHostBundleFixture(t, opts, hostBundleRun{hostID: "factor"})
	require.True(t, result.SignKeyIssued)
	pair, err := tls.LoadX509KeyPair(filepath.Join(result.BundleDir, hostbundle.HostCertificateFile), filepath.Join(result.BundleDir, hostbundle.HostTLSKeyFile))
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	require.NoError(t, err)
	require.Contains(t, leaf.ExtKeyUsage, x509.ExtKeyUsageClientAuth, "只能出站的主机要用证书建立反向连接")
}

func TestHostBundleRejectsInvalidArgumentsAndMissingState(t *testing.T) {
	opts := newBootstrapFixture(t)
	for _, args := range [][]string{
		{},
		{"host-bundle"},
		{"host-bundle", "--host-id", "Storage", "--control-host-id", "control", "--db-path", opts.dbPath, "--encryption-key-file", opts.masterFile, "--pki-dir", opts.pkiDir, "--output-dir", opts.outputDir},
		append(hostBundleArguments(opts, hostBundleRun{hostID: "storage"}), "extra"),
		append(hostBundleArguments(opts, hostBundleRun{hostID: "storage"}), "--current-sign-public-key", "bad"),
	} {
		require.Error(t, runHostBundleCommand(args, nil, nil), args)
	}
	// 还没 bootstrap：没有库、没有 CA，不签发也不新建任何东西。
	require.Error(t, runHostBundleCommand(hostBundleArguments(opts, hostBundleRun{hostID: "storage"}), nil, nil))
	for _, path := range []string{opts.dbPath, opts.pkiDir, opts.outputDir} {
		_, err := os.Lstat(path)
		require.True(t, os.IsNotExist(err), path)
	}
	bootstrap := runBootstrapFixture(t, opts)
	// CA 丢了：不签发，也不换掉已登记的公钥。
	require.NoError(t, os.Rename(opts.pkiDir, opts.pkiDir+".lost"))
	require.Error(t, runHostBundleCommand(hostBundleArguments(opts, hostBundleRun{hostID: "control"}), nil, nil))
	hosts, _ := registeredKeys(t, opts)
	require.Equal(t, bootstrap.SignPublicKey, hosts["control"])
	require.True(t, isHostBundleCommand([]string{"admin-cli", "host-bundle"}))
}
