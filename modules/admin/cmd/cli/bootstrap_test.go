package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/pki"
	"github.com/mooyang-code/moox/modules/admin/internal/privatefiles"
	"github.com/mooyang-code/moox/modules/admin/internal/service/keys"
	secretmodel "github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func newBootstrapFixture(t *testing.T) bootstrapOptions {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	require.NoError(t, os.Mkdir(dir, 0o700))
	opts := bootstrapOptions{topologyFile: filepath.Join(dir, "topology.json"), dbPath: filepath.Join(dir, "data", "admin.db"), masterFile: filepath.Join(dir, "secrets", "admin-encryption.key"), pkiDir: filepath.Join(dir, "secrets", "pki"), outputDir: filepath.Join(dir, "bundles")}
	writeBootstrapTopology(t, opts, bootstrapTopology{Version: 1, ControlHostID: "control", Hosts: []bootstrapHost{
		{HostID: "control", Address: "192.0.2.1", PrivateAddress: "control.internal.example.test", Components: []string{"admin", "console-proxy", "web-host", "collector"}},
		{HostID: "storage", Address: "192.0.2.2", Components: []string{"storage-primary", "storage-node", "storage-view"}},
		{HostID: "compute-1", Address: "192.0.2.3", Components: []string{"access", "trade"}},
	}})
	return opts
}

func writeBootstrapTopology(t *testing.T, opts bootstrapOptions, topology bootstrapTopology) {
	t.Helper()
	raw, err := json.Marshal(topology)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(opts.topologyFile, raw, 0o600))
}

func bootstrapArguments(opts bootstrapOptions) []string {
	return []string{"bootstrap", "--topology-file", opts.topologyFile, "--db-path", opts.dbPath, "--encryption-key-file", opts.masterFile, "--pki-dir", opts.pkiDir, "--output-dir", opts.outputDir}
}

func runBootstrapFixture(t *testing.T, opts bootstrapOptions) bootstrapResult {
	t.Helper()
	var out, stderr bytes.Buffer
	require.NoError(t, runBootstrapCommand(bootstrapArguments(opts), &out, &stderr), stderr.String())
	require.NotContains(t, out.String()+stderr.String(), "BEGIN")
	var result bootstrapResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))
	return result
}

func TestBootstrapEmptyEnvironmentExportsWorkingCredentialsCertificateAndConfig(t *testing.T) {
	opts := newBootstrapFixture(t)
	result := runBootstrapFixture(t, opts)
	require.True(t, result.CA.Created)
	require.Len(t, result.ExpectedHash, 64)
	operatorConfig := filepath.Join(result.BundleDir, "operator", "gateway-client.yaml")
	rawOperator, err := os.ReadFile(operatorConfig)
	require.NoError(t, err)
	var operator gatewayclient.FileConfig
	require.NoError(t, yaml.Unmarshal(rawOperator, &operator))
	require.Equal(t, "moox-cli", operator.Caller)
	var operatorKeyID string
	for _, credential := range result.Credentials {
		if credential.Caller == "moox-cli" {
			operatorKeyID = credential.KeyID
		}
	}
	require.NotEmpty(t, operatorKeyID)
	require.NotEqual(t, "moox-cli", operatorKeyID)
	require.Equal(t, operatorKeyID, operator.KeyID)
	require.Equal(t, "caller-moox-cli.key", operator.KeyFile)
	info, err := os.Stat(operatorConfig)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	config, err := hostgatewayconfig.Load(filepath.Join(result.BundleDir, result.ConfigFile))
	require.NoError(t, err)
	require.Equal(t, "control", config.Host.ID)
	require.Equal(t, "127.0.0.1:11112", config.Control.Target)
	key, err := gatewayauth.CredentialsFromKeyFile(config.Control.KeyID, config.Control.KeyFile)
	require.NoError(t, err)
	key.Caller = config.Control.Caller
	master, err := privatefiles.Read(opts.masterFile, 8192)
	require.NoError(t, err)
	db, err := openAdminCLIDB(opts.dbPath)
	require.NoError(t, err)
	defer closeAdminCLIDB(db)
	dao, err := sysdeploy.NewTopologyDAO(db, "control")
	require.NoError(t, err)
	hosts, placements, err := dao.Read(context.Background())
	require.NoError(t, err)
	require.Len(t, hosts, 3)
	require.Len(t, placements, 15)
	_, snapshot, err := dao.CompileSnapshot(context.Background(), "control", strings.TrimSpace(string(master)))
	require.NoError(t, err)
	require.Equal(t, result.ExpectedHash, snapshot.Hash)
	var verification []gatewayauth.Credentials
	for _, entry := range snapshot.VerificationKeys {
		verification = append(verification, gatewayauth.Credentials{Caller: entry.Caller, KeyID: entry.KeyId, Secret: string(entry.Secret)})
	}
	registry, err := gatewayauth.NewCredentialRegistry(verification)
	require.NoError(t, err)
	request := gatewayauth.Request{Method: "POST", Path: "/trpc.moox.admin.GatewayControl/PullSnapshot", TargetNode: "control", Callee: "trpc.moox.admin.GatewayControl", Func: "PullSnapshot", Body: []byte("fixture body")}
	header, err := gatewayauth.Sign(key, request, time.Now())
	require.NoError(t, err)
	claims, err := registry.Verify(request, header, time.Now())
	require.NoError(t, err)
	require.Equal(t, key.Caller, claims.Caller)
	rootPEM, err := os.ReadFile(config.TLS.CAFile)
	require.NoError(t, err)
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(rootPEM))
	pair, err := tls.LoadX509KeyPair(config.TLS.CertificateFile, config.TLS.KeyFile)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	require.NoError(t, err)
	for _, name := range []string{"control", "192.0.2.1", "control.internal.example.test"} {
		_, err := cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: name})
		require.NoError(t, err)
	}
	_, err = cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: "storage"})
	require.Error(t, err)
	metadata, err := os.ReadFile(filepath.Join(result.BundleDir, "bootstrap.json"))
	require.NoError(t, err)
	require.NotContains(t, string(metadata), key.Secret)
	require.NotContains(t, string(metadata), strings.TrimSpace(string(master)))
	for _, entry := range result.Credentials {
		require.NotContains(t, entry.Caller, "@storage")
		require.NotContains(t, entry.Caller, "@compute-1")
		require.False(t, slices.Contains([]string{"factor-engine", "scf-collector", "trade"}, entry.Caller))
	}
	require.NoError(t, filepath.WalkDir(result.BundleDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		require.NoError(t, err)
		want := os.FileMode(0o600)
		if entry.IsDir() {
			want = 0o700
		}
		require.Equal(t, want, info.Mode().Perm(), path)
		return nil
	}))
	block, _ := pem.Decode(rootPEM)
	require.NotNil(t, block)
	ca, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	require.True(t, ca.IsCA)
}

func TestBootstrapRetryKeepsMasterCAKeysAndDisabledStates(t *testing.T) {
	opts := newBootstrapFixture(t)
	first := runBootstrapFixture(t, opts)
	master, err := os.ReadFile(opts.masterFile)
	require.NoError(t, err)
	root, err := os.ReadFile(filepath.Join(opts.pkiDir, "ca.crt"))
	require.NoError(t, err)
	recovery := func(kind, host, status string, extra ...string) {
		t.Helper()
		args := append([]string{kind, "set-status", "--db-path", opts.dbPath, "--control-host-id", "control", "--host-id", host, "--status", status}, extra...)
		require.NoError(t, runTopologyRecoveryCommand(args, nil, nil))
	}
	recovery("host", "compute-1", "disabled")
	recovery("placement", "control", "disabled", "--component-id", "collector")
	again := runBootstrapFixture(t, opts)
	require.False(t, again.CA.Created)
	require.Equal(t, first.CA.CertificateInfo, again.CA.CertificateInfo)
	require.Equal(t, first.Credentials, again.Credentials)
	require.NotEqual(t, first.BundleDir, again.BundleDir)
	againMaster, err := os.ReadFile(opts.masterFile)
	require.NoError(t, err)
	require.Equal(t, master, againMaster)
	againRoot, err := os.ReadFile(filepath.Join(opts.pkiDir, "ca.crt"))
	require.NoError(t, err)
	require.Equal(t, root, againRoot)
	db, err := openAdminCLIDB(opts.dbPath)
	require.NoError(t, err)
	defer closeAdminCLIDB(db)
	dao, err := sysdeploy.NewTopologyDAO(db, "control")
	require.NoError(t, err)
	compiled, err := dao.Compile(context.Background(), "compute-1")
	require.NoError(t, err)
	require.True(t, compiled.Disabled)
	control, err := dao.Compile(context.Background(), "control")
	require.NoError(t, err)
	require.False(t, slices.ContainsFunc(control.Routes, func(r servicecatalog.Route) bool { return r.ComponentID == "collector" }))
	recovery("host", "compute-1", "enabled")
	recovery("placement", "control", "enabled", "--component-id", "collector")
	recovered := runBootstrapFixture(t, opts)
	require.Equal(t, first.ExpectedHash, recovered.ExpectedHash)
	require.Equal(t, first.Credentials, recovered.Credentials)
}

func TestBootstrapRejectsInvalidTopologyBeforeCreatingTrustMaterial(t *testing.T) {
	for _, mutate := range []func(*bootstrapTopology){
		func(t *bootstrapTopology) { t.Hosts[1].Components = append(t.Hosts[1].Components, "collector") },
		func(t *bootstrapTopology) { t.Hosts[0].Components = []string{"admin"} },
		func(t *bootstrapTopology) { t.Hosts[1].HostID = t.Hosts[0].HostID },
		func(t *bootstrapTopology) { t.Hosts[1].Address = "http://invalid" },
		func(t *bootstrapTopology) { t.Hosts[1].Components = []string{"host-gateway"} },
	} {
		opts := newBootstrapFixture(t)
		topology, err := readBootstrapTopology(opts.topologyFile)
		require.NoError(t, err)
		mutate(&topology)
		writeBootstrapTopology(t, opts, topology)
		var out bytes.Buffer
		require.Error(t, runBootstrapCommand(bootstrapArguments(opts), &out, nil))
		require.Empty(t, out.String())
		for _, path := range []string{opts.masterFile, opts.pkiDir, opts.outputDir} {
			_, err := os.Lstat(path)
			require.True(t, os.IsNotExist(err), path)
		}
		db, err := openAdminCLIDB(opts.dbPath)
		require.NoError(t, err)
		var count int64
		require.NoError(t, db.Table("t_hosts").Count(&count).Error)
		require.Zero(t, count)
		closeAdminCLIDB(db)
	}
}

func TestBootstrapRejectsLostOrCorruptTrustAndRollsBackTopology(t *testing.T) {
	for _, damage := range []string{"master-missing", "master-wrong", "ca-missing", "ca-corrupt"} {
		t.Run(damage, func(t *testing.T) {
			opts := newBootstrapFixture(t)
			first := runBootstrapFixture(t, opts)
			topology, err := readBootstrapTopology(opts.topologyFile)
			require.NoError(t, err)
			topology.Hosts[1].Description = "must roll back"
			writeBootstrapTopology(t, opts, topology)
			switch damage {
			case "master-missing":
				require.NoError(t, os.Remove(opts.masterFile))
			case "master-wrong":
				require.NoError(t, os.WriteFile(opts.masterFile, []byte("different-fixture-only-master-0123456789"), 0o600))
			case "ca-missing":
				require.NoError(t, os.RemoveAll(opts.pkiDir))
			case "ca-corrupt":
				require.NoError(t, os.WriteFile(filepath.Join(opts.pkiDir, "ca.crt"), []byte("broken fixture"), 0o600))
			}
			var out bytes.Buffer
			require.Error(t, runBootstrapCommand(bootstrapArguments(opts), &out, nil))
			require.Empty(t, out.String())
			files, err := os.ReadDir(opts.outputDir)
			require.NoError(t, err)
			require.Len(t, files, 1)
			require.Equal(t, filepath.Base(first.BundleDir), files[0].Name())
			db, err := openAdminCLIDB(opts.dbPath)
			require.NoError(t, err)
			var description string
			require.NoError(t, db.Table("t_hosts").Select("c_description").Where("c_host_id = 'storage'").Scan(&description).Error)
			require.Empty(t, description)
			closeAdminCLIDB(db)
			if damage == "master-missing" {
				_, err := os.Lstat(opts.masterFile)
				require.True(t, os.IsNotExist(err), "must not regenerate a missing master")
			}
			if damage == "ca-missing" {
				_, err := os.Lstat(opts.pkiDir)
				require.True(t, os.IsNotExist(err), "must not regenerate a lost CA")
			}
		})
	}
}

func TestBootstrapFailedOutputCanRetryWithoutRotatingIdentity(t *testing.T) {
	opts := newBootstrapFixture(t)
	require.NoError(t, os.Mkdir(opts.outputDir, 0o755))
	var out bytes.Buffer
	require.Error(t, runBootstrapCommand(bootstrapArguments(opts), &out, nil))
	require.Empty(t, out.String())
	files, err := os.ReadDir(opts.outputDir)
	require.NoError(t, err)
	require.Empty(t, files)
	master, err := privatefiles.Read(opts.masterFile, 8192)
	require.NoError(t, err)
	db, err := openAdminCLIDB(opts.dbPath)
	require.NoError(t, err)
	store, err := keys.NewStore(db, strings.TrimSpace(string(master)))
	require.NoError(t, err)
	key, err := store.Current(context.Background(), "host-gateway@control")
	require.NoError(t, err)
	closeAdminCLIDB(db)
	require.NoError(t, os.Chmod(opts.outputDir, 0o700))
	result := runBootstrapFixture(t, opts)
	require.False(t, result.CA.Created)
	require.True(t, slices.ContainsFunc(result.Credentials, func(c bootstrapCredential) bool { return c.Caller == key.Caller && c.KeyID == key.KeyID }))
}

func TestBootstrapDatabaseFailureRollsBackTopologyAndPartialKeys(t *testing.T) {
	opts := newBootstrapFixture(t)
	require.NoError(t, prepareBootstrapDatabase(opts.dbPath))
	require.NoError(t, ensureAdminSchema(opts.dbPath))
	db, err := openAdminCLIDB(opts.dbPath)
	require.NoError(t, err)
	defer closeAdminCLIDB(db)
	require.NoError(t, db.Exec(`CREATE TRIGGER fail_bootstrap_key BEFORE INSERT ON t_secrets
	WHEN NEW.c_secret_id = '`+secretmodel.GatewayKeyringIDPrefix+`storage-view'
BEGIN SELECT RAISE(ABORT, 'fixture key persistence failure'); END`).Error)
	var out bytes.Buffer
	require.Error(t, runBootstrapCommand(bootstrapArguments(opts), &out, nil))
	require.Empty(t, out.String())
	for _, table := range []string{"t_hosts", "t_placements", "t_secrets"} {
		var count int64
		require.NoError(t, db.Table(table).Count(&count).Error)
		require.Zero(t, count, table)
	}
	master, err := privatefiles.Read(opts.masterFile, 8192)
	require.NoError(t, err)
	for _, path := range []string{opts.pkiDir, opts.outputDir} {
		_, err := os.Lstat(path)
		require.True(t, os.IsNotExist(err))
	}
	require.NoError(t, db.Exec("DROP TRIGGER fail_bootstrap_key").Error)
	runBootstrapFixture(t, opts)
	again, err := privatefiles.Read(opts.masterFile, 8192)
	require.NoError(t, err)
	require.Equal(t, master, again)
}

func TestBootstrapBundleFailureCleansStageAndRetryReusesCommittedKeys(t *testing.T) {
	opts := newBootstrapFixture(t)
	ca, err := pki.Open(opts.pkiDir)
	require.NoError(t, err)
	info, err := ca.EnsureCA()
	require.NoError(t, err)
	require.NoError(t, ca.Close())
	// Issue writes the staged certificate before updating the public inventory.
	// An invalid inventory path exercises failure after private files exist.
	require.NoError(t, os.WriteFile(filepath.Join(opts.pkiDir, "hosts"), []byte("fixture"), 0o600))
	var out bytes.Buffer
	require.Error(t, runBootstrapCommand(bootstrapArguments(opts), &out, nil))
	require.Empty(t, out.String())
	files, err := os.ReadDir(opts.outputDir)
	require.NoError(t, err)
	require.Empty(t, files, "a failed bundle must leave no staging directory")
	require.NoError(t, os.Remove(filepath.Join(opts.pkiDir, "hosts")))
	result := runBootstrapFixture(t, opts)
	require.Equal(t, info.CertificateInfo, result.CA.CertificateInfo)
	require.False(t, result.CA.Created)
}

func TestBootstrapConcurrentProcessesReuseAllIdentityMaterial(t *testing.T) {
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
	var first bootstrapResult
	for i, err := range errors {
		require.NoError(t, err, outputs[i].String())
		var result bootstrapResult
		require.NoError(t, json.Unmarshal(outputs[i].Bytes(), &result))
		if first.CA.SHA256 == "" {
			first = result
		}
		require.Equal(t, first.CA.CertificateInfo, result.CA.CertificateInfo)
		require.Equal(t, first.ExpectedHash, result.ExpectedHash)
		require.Equal(t, first.Credentials, result.Credentials)
		if result.CA.Created {
			created++
		}
	}
	require.Equal(t, 1, created)
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
	for _, raw := range []string{
		`{"version":1,"control_host_id":"control","hosts":[],"ssh_password":"fixture-do-not-echo"}`,
		`{"version":1,"control_host_id":"control","hosts":[{"host_id":"storage"}]}`,
		`{"version":2,"control_host_id":"control","hosts":[]}`,
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
