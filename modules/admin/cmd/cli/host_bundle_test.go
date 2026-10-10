package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/privatefiles"
	"github.com/mooyang-code/moox/modules/admin/internal/service/keys"
	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"github.com/stretchr/testify/require"
)

func hostBundleArguments(opts bootstrapOptions, hostID string) []string {
	return []string{"host-bundle", "--host-id", hostID, "--control-host-id", "control", "--db-path", opts.dbPath, "--encryption-key-file", opts.masterFile, "--pki-dir", opts.pkiDir, "--output-dir", opts.outputDir}
}

func runHostBundleFixture(t *testing.T, opts bootstrapOptions, hostID string) hostBundleResult {
	t.Helper()
	var stdout, stderr bytes.Buffer
	require.NoError(t, runHostBundleCommand(hostBundleArguments(opts, hostID), &stdout, &stderr), stderr.String())
	var result hostBundleResult
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &result))
	metadata, err := os.ReadFile(filepath.Join(result.BundleDir, "bundle.json"))
	require.NoError(t, err)
	var saved hostBundleResult
	require.NoError(t, json.Unmarshal(metadata, &saved))
	require.Equal(t, result, saved)
	for _, name := range []string{opts.masterFile, filepath.Join(opts.pkiDir, "ca.key")} {
		secret, err := os.ReadFile(name)
		require.NoError(t, err)
		require.NotContains(t, stdout.String()+stderr.String()+string(metadata), strings.TrimSpace(string(secret)))
	}
	for _, c := range result.Credentials {
		secret, err := privatefiles.Read(filepath.Join(result.BundleDir, c.KeyFile), 8192)
		require.NoError(t, err)
		require.NotContains(t, string(metadata), strings.TrimSpace(string(secret)))
	}
	return result
}

func TestHostBundleScopesPrivateIdentityReissuesLeafAndPreservesTopology(t *testing.T) {
	opts := newBootstrapFixture(t)
	bootstrap := runBootstrapFixture(t, opts)
	master, err := privatefiles.Read(opts.masterFile, 8192)
	require.NoError(t, err)
	db, err := openAdminCLIDB(opts.dbPath)
	require.NoError(t, err)
	defer closeAdminCLIDB(db)
	dao, err := sysdeploy.NewTopologyDAO(db, "control")
	require.NoError(t, err)
	require.NoError(t, dao.SetHostStatus(context.Background(), "storage", "disabled"))
	require.NoError(t, dao.SetPlacementStatus(context.Background(), "compute-1", "access", "disabled"))
	beforeHosts, beforePlacements, err := dao.Read(context.Background())
	require.NoError(t, err)
	for _, hostID := range []string{"control", "storage", "compute-1"} {
		t.Run(hostID, func(t *testing.T) {
			first := runHostBundleFixture(t, opts, hostID)
			again := runHostBundleFixture(t, opts, hostID)
			require.Equal(t, hostID, first.HostID)
			require.Equal(t, "control", first.ControlHostID)
			require.False(t, first.CA.Created)
			require.Equal(t, bootstrap.CA.CertificateInfo, first.CA.CertificateInfo)
			require.Equal(t, first.Credentials, again.Credentials)
			require.NotEqual(t, first.BundleDir, again.BundleDir)
			require.NotEqual(t, first.Certificate.Serial, again.Certificate.Serial)
			require.NotEqual(t, first.Certificate.SHA256, again.Certificate.SHA256)
			_, snapshot, err := dao.CompileSnapshot(context.Background(), hostID, strings.TrimSpace(string(master)))
			require.NoError(t, err)
			require.Equal(t, snapshot.Hash, first.ExpectedHash)
			config, err := hostgatewayconfig.Load(filepath.Join(first.BundleDir, first.ConfigFile))
			require.NoError(t, err)
			require.Equal(t, hostID, config.Host.ID)
			expectedTarget := "192.0.2.1:11003"
			if hostID == "control" {
				expectedTarget = "127.0.0.1:11112"
			}
			require.Equal(t, expectedTarget, config.Control.Target)
			pair, err := tls.LoadX509KeyPair(config.TLS.CertificateFile, config.TLS.KeyFile)
			require.NoError(t, err)
			leaf, err := x509.ParseCertificate(pair.Certificate[0])
			require.NoError(t, err)
			roots := x509.NewCertPool()
			ca, err := os.ReadFile(config.TLS.CAFile)
			require.NoError(t, err)
			require.True(t, roots.AppendCertsFromPEM(ca))
			host := beforeHosts[slices.IndexFunc(beforeHosts, func(h sysdeploy.HostRecord) bool { return h.HostID == hostID })]
			for _, name := range []string{host.HostID, host.Address, host.PrivateAddress} {
				if name != "" {
					_, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: name})
					require.NoError(t, err)
				}
			}
			require.Error(t, leaf.VerifyHostname("unrelated-host"))
			want := []string{"host-agent", "host-gateway@" + hostID}
			for _, p := range beforePlacements {
				if p.HostID == hostID && p.ComponentID != "host-agent" && p.ComponentID != "host-gateway" {
					want = append(want, p.ComponentID)
				}
			}
			if hostID == "control" {
				want = append(want, "console")
			}
			var got []string
			for _, c := range first.Credentials {
				got = append(got, c.Caller)
			}
			require.ElementsMatch(t, want, got)
			_, err = os.Lstat(filepath.Join(first.BundleDir, "operator"))
			require.True(t, os.IsNotExist(err), "normal deployment never exports the operator identity")
			fileCount := 0
			require.NoError(t, filepath.WalkDir(first.BundleDir, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				info, err := entry.Info()
				require.NoError(t, err)
				mode := os.FileMode(0o600)
				if entry.IsDir() {
					mode = 0o700
				} else {
					fileCount++
				}
				require.Equal(t, mode, info.Mode().Perm())
				require.Zero(t, info.Mode()&os.ModeSymlink)
				require.NotEqual(t, "ca.key", entry.Name())
				require.NotEqual(t, "admin-encryption.key", entry.Name())
				return nil
			}))
			require.Equal(t, fileCount-1, len(first.Files), "all payload files are hashed; bundle.json describes the inventory")
			for _, file := range first.Files {
				raw, err := os.ReadFile(filepath.Join(first.BundleDir, file.Path))
				require.NoError(t, err)
				digest := sha256.Sum256(raw)
				require.Equal(t, hex.EncodeToString(digest[:]), file.SHA256)
				require.EqualValues(t, len(raw), file.Size)
			}
			if hostID == "compute-1" {
				require.Equal(t, "secrets/access/access-verification.json", first.VerificationFile)
			} else {
				require.Empty(t, first.VerificationFile)
			}
		})
	}
	afterHosts, afterPlacements, err := dao.Read(context.Background())
	require.NoError(t, err)
	require.Equal(t, beforeHosts, afterHosts)
	require.Equal(t, beforePlacements, afterPlacements)
}

func TestHostBundleExportsOnlyExternalAccessKeysAcrossRotation(t *testing.T) {
	opts := newBootstrapFixture(t)
	runBootstrapFixture(t, opts)
	master, err := privatefiles.Read(opts.masterFile, 8192)
	require.NoError(t, err)
	db, err := openAdminCLIDB(opts.dbPath)
	require.NoError(t, err)
	defer closeAdminCLIDB(db)
	store, err := keys.NewStore(db, strings.TrimSpace(string(master)))
	require.NoError(t, err)
	old, err := store.Current(context.Background(), "scf-collector")
	require.NoError(t, err)
	current, err := store.Rotate(context.Background(), "scf-collector")
	require.NoError(t, err)
	first := runHostBundleFixture(t, opts, "compute-1")
	registryPath := filepath.Join(first.BundleDir, first.VerificationFile)
	registry, err := gatewayauth.LoadCredentialRegistry(registryPath)
	require.NoError(t, err)
	request := gatewayauth.Request{Method: "POST", Path: "/trpc.moox.collector.MarketFetchRuntime/ClaimTimerBatch", TargetNode: "access@compute-1", Callee: "trpc.moox.collector.MarketFetchRuntime", Func: "ClaimTimerBatch"}
	verify := func(registry *gatewayauth.CredentialRegistry, key keys.SigningKey) error {
		header, err := gatewayauth.Sign(key.Credentials(), request, time.Now())
		require.NoError(t, err)
		_, err = registry.Verify(request, header, time.Now())
		return err
	}
	require.NoError(t, verify(registry, old))
	require.NoError(t, verify(registry, current))
	internal, err := store.Current(context.Background(), "trade")
	require.NoError(t, err)
	require.Error(t, verify(registry, internal))
	raw, err := os.ReadFile(registryPath)
	require.NoError(t, err)
	for _, caller := range []string{"console", "moox-cli", "host-gateway@", "trade"} {
		require.NotContains(t, string(raw), caller)
	}
	require.NoError(t, store.Retire(context.Background(), "scf-collector", old.KeyID))
	again := runHostBundleFixture(t, opts, "compute-1")
	registry, err = gatewayauth.LoadCredentialRegistry(filepath.Join(again.BundleDir, again.VerificationFile))
	require.NoError(t, err)
	require.Error(t, verify(registry, old))
	require.NoError(t, verify(registry, current))
}

func TestHostBundleProvisionsNewlyRegisteredHostAndUsesPersistedAddresses(t *testing.T) {
	opts := newBootstrapFixture(t)
	runBootstrapFixture(t, opts)
	db, err := openAdminCLIDB(opts.dbPath)
	require.NoError(t, err)
	defer closeAdminCLIDB(db)
	dao, err := sysdeploy.NewTopologyDAO(db, "control")
	require.NoError(t, err)
	require.NoError(t, dao.SyncHostPlacements(context.Background(), sysdeploy.HostSpec{HostID: "compute-2", Address: "192.0.2.90", PrivateAddress: "2001:db8::90"}))
	first := runHostBundleFixture(t, opts, "compute-2")
	require.Len(t, first.Credentials, 2)
	require.NoError(t, dao.SyncHostPlacements(context.Background(), sysdeploy.HostSpec{HostID: "compute-2", Address: "compute-2.example.test", PrivateAddress: "2001:db8::91"}))
	again := runHostBundleFixture(t, opts, "compute-2")
	require.Equal(t, first.Credentials, again.Credentials)
	config, err := hostgatewayconfig.Load(filepath.Join(again.BundleDir, again.ConfigFile))
	require.NoError(t, err)
	pair, err := tls.LoadX509KeyPair(config.TLS.CertificateFile, config.TLS.KeyFile)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	require.NoError(t, err)
	for _, name := range []string{"compute-2", "compute-2.example.test", "2001:db8::91"} {
		require.NoError(t, leaf.VerifyHostname(name))
	}
	require.Error(t, leaf.VerifyHostname("192.0.2.90"))
}

func TestHostBundleRejectsMissingOrCorruptStateWithoutPublishingOrRegenerating(t *testing.T) {
	for _, damage := range []string{"missing-db", "wrong-control", "unknown-host", "missing-master", "wrong-master", "unsafe-master", "symlink-master", "missing-ca", "corrupt-ca", "unsafe-output"} {
		t.Run(damage, func(t *testing.T) {
			opts := newBootstrapFixture(t)
			runBootstrapFixture(t, opts)
			opts.outputDir = filepath.Join(filepath.Dir(opts.outputDir), "target-bundles")
			args := hostBundleArguments(opts, "storage")
			switch damage {
			case "missing-db":
				require.NoError(t, os.Remove(opts.dbPath))
			case "wrong-control":
				args[4] = "storage"
			case "unknown-host":
				args[2] = "not-registered"
			case "missing-master":
				require.NoError(t, os.Remove(opts.masterFile))
			case "wrong-master":
				require.NoError(t, os.WriteFile(opts.masterFile, []byte("wrong-fixture-master-at-least-32-bytes"), 0o600))
			case "unsafe-master":
				require.NoError(t, os.Chmod(opts.masterFile, 0o644))
			case "symlink-master":
				require.NoError(t, os.Rename(opts.masterFile, opts.masterFile+".original"))
				require.NoError(t, os.Symlink(opts.masterFile+".original", opts.masterFile))
			case "missing-ca":
				require.NoError(t, os.RemoveAll(opts.pkiDir))
			case "corrupt-ca":
				require.NoError(t, os.WriteFile(filepath.Join(opts.pkiDir, "ca.crt"), []byte("broken fixture"), 0o600))
			case "unsafe-output":
				require.NoError(t, os.Mkdir(opts.outputDir, 0o755))
			}
			var stdout bytes.Buffer
			require.Error(t, runHostBundleCommand(args, &stdout, nil))
			require.Empty(t, stdout.String())
			files, err := os.ReadDir(opts.outputDir)
			if !os.IsNotExist(err) {
				require.NoError(t, err)
				require.Empty(t, files)
			}
			if damage == "missing-ca" {
				for _, name := range []string{"ca.crt", "ca.key"} {
					_, err := os.Lstat(filepath.Join(opts.pkiDir, name))
					require.True(t, os.IsNotExist(err))
				}
			}
			if damage == "missing-master" || damage == "missing-db" {
				path := opts.masterFile
				if damage == "missing-db" {
					path = opts.dbPath
				}
				_, err := os.Lstat(path)
				require.True(t, os.IsNotExist(err))
			}
		})
	}
}

func TestHostBundlePartialOutputFailureCleansStageAndRetriesCommittedIdentity(t *testing.T) {
	opts := newBootstrapFixture(t)
	runBootstrapFixture(t, opts)
	// Force failure after certificate material has been staged, when its public
	// watch inventory is written. The previous complete bootstrap stays intact.
	path := filepath.Join(opts.pkiDir, "hosts", "storage.crt")
	require.NoError(t, os.Mkdir(path, 0o700))
	var stdout bytes.Buffer
	require.Error(t, runHostBundleCommand(hostBundleArguments(opts, "storage"), &stdout, nil))
	require.Empty(t, stdout.String())
	files, err := os.ReadDir(opts.outputDir)
	require.NoError(t, err)
	require.Len(t, files, 1)
	require.NoError(t, os.Remove(path))
	first := runHostBundleFixture(t, opts, "storage")
	again := runHostBundleFixture(t, opts, "storage")
	require.Equal(t, first.Credentials, again.Credentials)
}

func TestHostBundleRejectsInvalidArgumentsBeforeOpeningState(t *testing.T) {
	opts := newBootstrapFixture(t)
	valid := hostBundleArguments(opts, "storage")
	for _, args := range [][]string{
		nil,
		{"bootstrap"},
		{"host-bundle"},
		append(slices.Clone(valid), "unexpected"),
		append(slices.Clone(valid), "--address", "192.0.2.1"),
		append(slices.Clone(valid), "--host-id", "../storage"),
		append(slices.Clone(valid), "--control-host-id", "Control"),
		append(slices.Clone(valid), "--db-path", "file:invalid"),
		append(slices.Clone(valid), "--output-dir", " target "),
		append(slices.Clone(valid), "--encryption-key-file", opts.dbPath),
	} {
		var stdout bytes.Buffer
		require.Error(t, runHostBundleCommand(args, &stdout, nil))
		require.Empty(t, stdout.String())
		for _, path := range []string{opts.dbPath, opts.masterFile, opts.pkiDir, opts.outputDir} {
			_, err := os.Lstat(path)
			require.True(t, os.IsNotExist(err), path)
		}
	}
}

func TestHostBundleConcurrentProcessesReuseKeysAndIssueDistinctLeaves(t *testing.T) {
	opts := newBootstrapFixture(t)
	runBootstrapFixture(t, opts)
	executable, err := os.Executable()
	require.NoError(t, err)
	raw, err := json.Marshal(hostBundleArguments(opts, "compute-1"))
	require.NoError(t, err)
	const writers = 3
	var outputs [writers]bytes.Buffer
	var errs [writers]error
	var wait sync.WaitGroup
	for i := range writers {
		command := exec.Command(executable, "-test.run=^TestHostBundleSubprocess$")
		command.Env = append(os.Environ(), "MOOX_TEST_HOST_BUNDLE_ARGS="+string(raw))
		command.Stdout, command.Stderr = &outputs[i], &outputs[i]
		wait.Go(func() { errs[i] = command.Run() })
	}
	wait.Wait()
	var first hostBundleResult
	serials := map[string]bool{}
	for i, err := range errs {
		require.NoError(t, err, outputs[i].String())
		var result hostBundleResult
		require.NoError(t, json.Unmarshal(outputs[i].Bytes(), &result))
		if i == 0 {
			first = result
		}
		require.Equal(t, first.CA, result.CA)
		require.Equal(t, first.Credentials, result.Credentials)
		require.Equal(t, first.ExpectedHash, result.ExpectedHash)
		require.False(t, serials[result.Certificate.Serial])
		serials[result.Certificate.Serial] = true
	}
}

func TestHostBundleSubprocess(t *testing.T) {
	raw := os.Getenv("MOOX_TEST_HOST_BUNDLE_ARGS")
	if raw == "" {
		return
	}
	var args []string
	if json.Unmarshal([]byte(raw), &args) != nil {
		os.Exit(2)
	}
	if err := runHostBundleCommand(args, os.Stdout, os.Stderr); err != nil {
		printInitError(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}
