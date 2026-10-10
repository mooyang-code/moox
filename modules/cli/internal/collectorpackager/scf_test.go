package collectorpackager

import (
	"archive/zip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"debug/elf"
	"encoding/pem"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/testfixture"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildSCFPackageRequiresBinaryAndConfig(t *testing.T) {
	_, err := BuildSCFPackage(BuildSCFPackageOptions{ConfigDir: "cfg", OutPath: "out.zip"})
	require.ErrorContains(t, err, "binary path is required")
}

func TestBuildSCFPackageExcludesTRPCAndCLSCredentials(t *testing.T) {
	tmp := t.TempDir()
	binary := filepath.Join(tmp, "main")
	require.NoError(t, os.WriteFile(binary, testfixture.LinuxExecutable(elf.EM_X86_64), 0o755))
	config := filepath.Join(tmp, "config")
	require.NoError(t, os.MkdirAll(filepath.Join(config, "sources", "example"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(config, "sources"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(config, "trpc_go.yaml"), []byte("secret: should-not-package\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(config, "sources", "example", "source.yaml"), []byte("kind: example\n"), 0o644))
	out := filepath.Join(tmp, "package.zip")
	result, err := BuildSCFPackage(BuildSCFPackageOptions{EventBusCAPEM: testCAPEM(t), BinaryPath: binary, ConfigDir: config, OutPath: out})
	require.NoError(t, err)
	assert.Equal(t, []string{"certs/eventbus-ca.pem", "main", "sources/example/source.yaml"}, result.Entries)
	reader, err := zip.OpenReader(out)
	require.NoError(t, err)
	defer reader.Close()
	for _, file := range reader.File {
		assert.NotContains(t, file.Name, "trpc_go.yaml")
		assert.NotContains(t, file.Name, "secret")
	}
}

func TestBuildSCFPackageIncludesStockCNCalendar(t *testing.T) {
	tmp := t.TempDir()
	binary := filepath.Join(tmp, "main")
	require.NoError(t, os.WriteFile(binary, testfixture.LinuxExecutable(elf.EM_X86_64), 0o755))
	config := filepath.Join(tmp, "modules", "collector", "configs", "scf", "stockcn")
	require.NoError(t, os.MkdirAll(filepath.Join(config, "sources"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(config, "sources"), 0o755))
	calendar := filepath.Join(tmp, "modules", "collector", "config", "markets", "stockcn", "calendar.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(calendar), 0o755))
	require.NoError(t, os.WriteFile(calendar, []byte("timezone: Asia/Shanghai\n"), 0o644))
	route := filepath.Join(tmp, "modules", "collector", "config", "markets", "stockcn", "route.yaml")
	require.NoError(t, os.WriteFile(route, []byte("market_id: stockcn\nroute_id: test\nfrequency: 1m\n"), 0o644))

	out := filepath.Join(tmp, "package.zip")
	result, err := BuildSCFPackage(BuildSCFPackageOptions{EventBusCAPEM: testCAPEM(t), BinaryPath: binary, ConfigDir: config, OutPath: out})
	require.NoError(t, err)
	assert.Contains(t, result.Entries, "markets/stockcn/calendar.yaml")
	assert.Contains(t, result.Entries, "markets/stockcn/route.yaml")

	reader, err := zip.OpenReader(out)
	require.NoError(t, err)
	defer reader.Close()
	var found bool
	for _, file := range reader.File {
		if file.Name == "markets/stockcn/calendar.yaml" {
			found = true
		}
	}
	assert.True(t, found)
	var routeFound bool
	for _, file := range reader.File {
		if file.Name == "markets/stockcn/route.yaml" {
			routeFound = true
		}
	}
	assert.True(t, routeFound)
}

func TestBuildSCFPackageRequiresStockCNCalendar(t *testing.T) {
	tmp := t.TempDir()
	binary := filepath.Join(tmp, "main")
	require.NoError(t, os.WriteFile(binary, testfixture.LinuxExecutable(elf.EM_X86_64), 0o755))
	config := filepath.Join(tmp, "modules", "collector", "configs", "scf", "stockcn")
	require.NoError(t, os.MkdirAll(config, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(config, "sources"), 0o755))

	_, err := BuildSCFPackage(BuildSCFPackageOptions{EventBusCAPEM: testCAPEM(t), BinaryPath: binary, ConfigDir: config, OutPath: filepath.Join(tmp, "package.zip")})
	require.ErrorContains(t, err, "stockcn calendar")
}

func TestBuildSCFPackageSetsOutputMode0600(t *testing.T) {
	tmp := t.TempDir()
	binary := filepath.Join(tmp, "main")
	config := filepath.Join(tmp, "config")
	out := filepath.Join(tmp, "package.zip")
	require.NoError(t, os.WriteFile(binary, testfixture.LinuxExecutable(elf.EM_X86_64), 0o755))
	require.NoError(t, os.MkdirAll(config, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(config, "sources"), 0o755))
	require.NoError(t, os.WriteFile(out, []byte("old"), 0o644))

	_, err := BuildSCFPackage(BuildSCFPackageOptions{EventBusCAPEM: testCAPEM(t), BinaryPath: binary, ConfigDir: config, OutPath: out})
	require.NoError(t, err)
	info, err := os.Stat(out)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestBuildSCFPackageDoesNotRenderStorageAuth(t *testing.T) {
	tmp := t.TempDir()
	binary := filepath.Join(tmp, "main")
	config := filepath.Join(tmp, "config")
	out := filepath.Join(tmp, "package.zip")
	require.NoError(t, os.WriteFile(binary, testfixture.LinuxExecutable(elf.EM_X86_64), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(config, "sources", "market"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(config, "sources"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(config, "sources", "market", "binance.yaml"), []byte(`
storage:
  bindings:
    spot:
      auth_info:
        app_id: "moox-collector"
        app_key: ""
`), 0o644))

	_, err := BuildSCFPackage(BuildSCFPackageOptions{EventBusCAPEM: testCAPEM(t), BinaryPath: binary, ConfigDir: config, OutPath: out})
	require.NoError(t, err)
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "test-storage-secret")
	t.Setenv("MOOX_EVENTBUS_NATS_PASSWORD", "test-eventbus-password")

	_, err = BuildSCFPackage(BuildSCFPackageOptions{
		EventBusCAPEM: testCAPEM(t),
		BinaryPath:    binary,
		ConfigDir:     config,
		OutPath:       out,
	})
	require.NoError(t, err)
	require.NoError(t, ValidateSCFPackageZip(out))
	reader, err := zip.OpenReader(out)
	require.NoError(t, err)
	defer reader.Close()
	for _, entry := range reader.File {
		rc, err := entry.Open()
		require.NoError(t, err)
		content, err := io.ReadAll(rc)
		require.NoError(t, err)
		require.NoError(t, rc.Close())
		assert.NotContains(t, string(content), "test-storage-secret")
		assert.NotContains(t, string(content), "test-eventbus-password")
	}
}

func TestValidateSCFPackageZipRejectsPlaceholderAuth(t *testing.T) {
	tmp := t.TempDir()
	out := filepath.Join(tmp, "package.zip")
	file, err := os.Create(out)
	require.NoError(t, err)
	zw := zip.NewWriter(file)
	writer, err := zw.Create("sources/market/binance.yaml")
	require.NoError(t, err)
	_, err = writer.Write([]byte(`
storage:
  bindings:
    spot:
      auth_info:
        app_id: "moox-collector"
        app_key: "binance-spot-collector"
`))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	require.NoError(t, file.Close())
	require.ErrorContains(t, ValidateSCFPackageZip(out), "app_key")
}

func TestValidateSCFPackageZipRejectsSecretsAndInvalidCA(t *testing.T) {
	for _, tc := range []struct{ name, path, payload string }{
		{"secret", "sources/market/extra.yaml", "secret: test-secret\n"},
		{"password", "sources/market/extra.yaml", "password: test-password\n"},
		{"nats jwt block scalar", "sources/market/extra.yaml", "credentials: |\n  -----BEGIN NATS USER JWT-----\n  test-user-jwt\n  ------END NATS USER JWT------\n"},
		{"nkey seed", "sources/market/extra.yaml", "nats:\n  user_nkey_seed: SUABCDEF1234567890\n"},
		{"nats jwt scalar", "sources/market/extra.yaml", "nats_user_jwt: eyJ0eXAiOiJKV1Qi.signature.payload\n"},
		{"hmac", "sources/market/binance.yaml", "app_key: " + strings.Repeat("a", 64) + "\n"},
		{"private", "certs/eventbus-ca.pem", "-----BEGIN PRIVATE KEY-----\nsecret\n-----END PRIVATE KEY-----\n"},
		{"malformed", "certs/eventbus-ca.pem", "not a certificate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTestPackage(t, map[string][]byte{"certs/eventbus-ca.pem": testCAPEM(t), tc.path: []byte(tc.payload)})
			err := ValidateSCFPackageZip(path)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "test-secret")
			assert.NotContains(t, err.Error(), "test-password")
		})
	}
}

func TestValidateSCFPackageZipRejectsCredentialAssignmentsInScalarsAndLists(t *testing.T) {
	for _, payload := range []string{
		"note: MOOX_STORAGE_PRIMARY_AUTH_SECRET=topsecret\n",
		"notes:\n  - MOOX_EVENTBUS_NATS_PASSWORD=secret\n",
		"MOOX_STORAGE_PRIMARY_AUTH_SECRET=topsecret:\n",
		"# MOOX_STORAGE_PRIMARY_AUTH_SECRET=topsecret\n",
		"# \"MOOX_STORAGE_PRIMARY_AUTH_SECRET\": \"topsecret\"\n",
		"# \"MOOX_STORAGE_PRIMARY_AUTH_SECRET\": \",topsecret\"\n",
		"# MOOX_STORAGE_PRIMARY_AUTH_SECRET: \"\"topsecret\n",
		"# MOOX_STORAGE_PRIMARY_AUTH_SECRET: null topsecret\n",
		"# MOOX_STORAGE_PRIMARY_AUTH_SECRET: ~ topsecret\n",
		"# MOOX_STORAGE_PRIMARY_AUTH_SECRET: null # MOOX_EVENTBUS_NATS_PASSWORD=topsecret\n",
		"# MOOX_STORAGE_PRIMARY_AUTH_SECRET: \"\" # MOOX_EVENTBUS_NATS_PASSWORD=topsecret\n",
		"env:\n  - name: MOOX_STORAGE_PRIMARY_AUTH_SECRET\n    value: topsecret\n",
		"env:\n  - Name: MOOX_EVENTBUS_NATS_PASSWORD\n    Value: topsecret\n",
	} {
		path := writeTestPackage(t, map[string][]byte{
			"certs/eventbus-ca.pem":     testCAPEM(t),
			"main":                      testfixture.LinuxExecutable(elf.EM_X86_64),
			"sources/market/extra.yaml": []byte(payload),
		})
		require.ErrorContains(t, ValidateSCFPackageZip(path), "credential")
	}
}

func TestValidateSCFPackageZipAllowsEmptyCredentialPlaceholders(t *testing.T) {
	for _, payload := range []string{
		"MOOX_STORAGE_PRIMARY_AUTH_SECRET: null\n",
		"MOOX_STORAGE_PRIMARY_AUTH_SECRET: ~\n",
		"MOOX_STORAGE_PRIMARY_AUTH_SECRET: \"\"\n",
		"MOOX_STORAGE_PRIMARY_AUTH_SECRET: '  '\n",
		"MOOX_STORAGE_PRIMARY_AUTH_SECRET:",
	} {
		path := writeTestPackage(t, map[string][]byte{
			"certs/eventbus-ca.pem":     testCAPEM(t),
			"main":                      testfixture.LinuxExecutable(elf.EM_X86_64),
			"sources/market/extra.yaml": []byte(payload),
		})
		require.NoError(t, ValidateSCFPackageZip(path), "placeholder %q should remain non-sensitive", payload)
	}
}

func TestBuildSCFPackageRejectsSymlinkedConfigDirectory(t *testing.T) {
	tmp := t.TempDir()
	config := filepath.Join(tmp, "config")
	linkedConfig := filepath.Join(tmp, "linked-config")
	binary := filepath.Join(tmp, "main")
	out := filepath.Join(tmp, "package.zip")
	require.NoError(t, os.MkdirAll(filepath.Join(config, "sources"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(config, "sources"), 0o755))
	require.NoError(t, os.WriteFile(binary, testfixture.LinuxExecutable(elf.EM_X86_64), 0o755))
	require.NoError(t, os.Symlink(config, linkedConfig))

	_, err := BuildSCFPackage(BuildSCFPackageOptions{BinaryPath: binary, ConfigDir: linkedConfig + string(filepath.Separator), OutPath: out, EventBusCAPEM: testCAPEM(t)})
	require.ErrorContains(t, err, "config dir")
	_, statErr := os.Stat(out)
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestBuildSCFPackageRejectsSymlinkedSourceFiles(t *testing.T) {
	tmp := t.TempDir()
	config := filepath.Join(tmp, "config")
	secret := filepath.Join(tmp, "outside.yaml")
	binary := filepath.Join(tmp, "main")
	out := filepath.Join(tmp, "package.zip")
	require.NoError(t, os.MkdirAll(filepath.Join(config, "sources", "market"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(config, "sources"), 0o755))
	require.NoError(t, os.WriteFile(secret, []byte("note: external-private-data\n"), 0o600))
	require.NoError(t, os.Symlink(secret, filepath.Join(config, "sources", "market", "leak.yaml")))
	require.NoError(t, os.WriteFile(binary, testfixture.LinuxExecutable(elf.EM_X86_64), 0o755))

	_, err := BuildSCFPackage(BuildSCFPackageOptions{BinaryPath: binary, ConfigDir: config, OutPath: out, EventBusCAPEM: testCAPEM(t)})
	require.ErrorContains(t, err, "symlink")
	_, statErr := os.Stat(out)
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestBuildSCFPackageDoesNotLeaveInvalidOutput(t *testing.T) {
	tmp := t.TempDir()
	config := filepath.Join(tmp, "config")
	require.NoError(t, os.MkdirAll(filepath.Join(config, "sources"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(config, "sources"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(config, "sources", "credentials.yaml"), []byte("credentials: |\n  -----BEGIN NATS USER JWT-----\n  test-user-jwt\n  ------END NATS USER JWT------\n"), 0o644))
	binary := filepath.Join(tmp, "main")
	require.NoError(t, os.WriteFile(binary, testfixture.LinuxExecutable(elf.EM_X86_64), 0o755))
	out := filepath.Join(tmp, "invalid.zip")

	_, err := BuildSCFPackage(BuildSCFPackageOptions{BinaryPath: binary, ConfigDir: config, OutPath: out, EventBusCAPEM: testCAPEM(t)})
	require.ErrorContains(t, err, "credential")
	_, statErr := os.Stat(out)
	assert.ErrorIs(t, statErr, os.ErrNotExist, "failed validation must not leave a publishable ZIP")
}

func TestValidateSCFPackageZipRequiresCA(t *testing.T) {
	require.ErrorContains(t, ValidateSCFPackageZip(writeTestPackage(t, map[string][]byte{"main": testfixture.LinuxExecutable(elf.EM_X86_64)})), "certs/eventbus-ca.pem")
}

func TestBuildSCFPackageIncludesExactPublicCA(t *testing.T) {
	tmp := t.TempDir()
	binary, config := filepath.Join(tmp, "main"), filepath.Join(tmp, "config")
	require.NoError(t, os.WriteFile(binary, testfixture.LinuxExecutable(elf.EM_X86_64), 0o755))
	require.NoError(t, os.MkdirAll(config, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(config, "sources"), 0o755))
	ca := testCAPEM(t)
	opts := BuildSCFPackageOptions{BinaryPath: binary, ConfigDir: config, OutPath: filepath.Join(tmp, "package.zip"), EventBusCAPEM: ca}
	_, err := BuildSCFPackage(opts)
	require.NoError(t, err)
	reader, err := zip.OpenReader(opts.OutPath)
	require.NoError(t, err)
	defer reader.Close()
	var found bool
	for _, file := range reader.File {
		if file.Name == "certs/eventbus-ca.pem" {
			found = true
			rc, err := file.Open()
			require.NoError(t, err)
			content, err := io.ReadAll(rc)
			require.NoError(t, err)
			require.NoError(t, rc.Close())
			assert.Equal(t, ca, content)
		}
	}
	require.True(t, found)
	for _, invalid := range [][]byte{nil, []byte("not PEM"), append(append([]byte{}, ca...), []byte("password: test-password")...)} {
		opts.EventBusCAPEM = invalid
		_, err := BuildSCFPackage(opts)
		require.Error(t, err)
	}
}

func TestBuildSCFPackageRealProfilesAreCredentialFree(t *testing.T) {
	for _, profile := range []string{"market_data", "stockcn"} {
		t.Run(profile, func(t *testing.T) {
			t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "test-profile-storage-secret")
			t.Setenv("MOOX_EVENTBUS_NATS_PASSWORD", "test-profile-eventbus-password")
			tmp := t.TempDir()
			binary := filepath.Join(tmp, "main")
			require.NoError(t, os.WriteFile(binary, testfixture.LinuxExecutable(elf.EM_X86_64), 0o755))
			config := filepath.Join("..", "..", "..", "collector", "configs", "scf", profile)
			result, err := BuildSCFPackage(BuildSCFPackageOptions{BinaryPath: binary, ConfigDir: config, OutPath: filepath.Join(tmp, "package.zip"), EventBusCAPEM: testCAPEM(t)})
			require.NoError(t, err)
			require.NoError(t, ValidateSCFPackageZip(result.Path))
			reader, err := zip.OpenReader(result.Path)
			require.NoError(t, err)
			defer reader.Close()
			for _, file := range reader.File {
				rc, err := file.Open()
				require.NoError(t, err)
				content, err := io.ReadAll(rc)
				require.NoError(t, err)
				require.NoError(t, rc.Close())
				assert.NotContains(t, string(content), "test-profile-storage-secret")
				assert.NotContains(t, string(content), "test-profile-eventbus-password")
			}
		})
	}
}

func TestValidateSCFPackageZipRejectsDuplicatePaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "duplicate.zip")
	file, err := os.Create(path)
	require.NoError(t, err)
	zw := zip.NewWriter(file)
	for range 2 {
		require.NoError(t, addZipBytes(zw, testCAPEM(t), "certs/eventbus-ca.pem", 0o644))
	}
	require.NoError(t, zw.Close())
	require.NoError(t, file.Close())
	require.ErrorContains(t, ValidateSCFPackageZip(path), "duplicate")
}

func TestValidateSCFPackageZipRejectsAccessKeyOutsideServiceIdentity(t *testing.T) {
	require.Error(t, ValidateSCFPackageZip(writeTestPackage(t, map[string][]byte{"certs/eventbus-ca.pem": testCAPEM(t), "sources/market/extra.yaml": []byte("access_key: test-credential\n")})))
}

func TestValidateSCFPackageZipRejectsSkippedMalformedCA(t *testing.T) {
	ca := append([]byte("-----BEGIN CERTIFICATE-----\ninvalid\n-----END CERTIFICATE-----\n"), testCAPEM(t)...)
	require.Error(t, ValidateSCFPackageZip(writeTestPackage(t, map[string][]byte{"certs/eventbus-ca.pem": ca})))
}

func TestValidateSCFPackageZipRequiresCAFileNotDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "directory.zip")
	file, err := os.Create(path)
	require.NoError(t, err)
	zw := zip.NewWriter(file)
	header := &zip.FileHeader{Name: "certs/eventbus-ca.pem"}
	header.SetMode(os.ModeDir | 0o755)
	_, err = zw.CreateHeader(header)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	require.NoError(t, file.Close())
	require.Error(t, ValidateSCFPackageZip(path))
}

func TestValidateSCFPackageZipRequiresExecutableLinuxMain(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content []byte
		mode    os.FileMode
	}{
		{"text", []byte("binary"), 0o755},
		{"empty", nil, 0o755},
		{"not executable", testfixture.LinuxExecutable(elf.EM_X86_64), 0o644},
		{"wrong architecture", testfixture.LinuxExecutable(elf.EM_386), 0o755},
		{"directory", nil, os.ModeDir | 0o755},
		{"private PEM", append(testfixture.LinuxExecutable(elf.EM_X86_64), []byte("\n-----BEGIN PRIVATE KEY-----\nprivate-test-secret\n-----END PRIVATE KEY-----\n")...), 0o755},
		{"credential assignment", append(testfixture.LinuxExecutable(elf.EM_X86_64), []byte("\nMOOX_EVENTBUS_NATS_PASSWORD=private-test-secret\n")...), 0o755},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "package.zip")
			file, err := os.Create(path)
			require.NoError(t, err)
			zw := zip.NewWriter(file)
			require.NoError(t, addZipBytes(zw, tc.content, "main", tc.mode))
			require.NoError(t, addZipBytes(zw, testCAPEM(t), "certs/eventbus-ca.pem", 0o644))
			require.NoError(t, zw.Close())
			require.NoError(t, file.Close())
			err = ValidateSCFPackageZip(path)
			require.ErrorContains(t, err, "main")
			require.NotContains(t, err.Error(), "private-test-secret")
		})
	}
	for _, machine := range []elf.Machine{elf.EM_X86_64, elf.EM_AARCH64} {
		t.Run(machine.String(), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "package.zip")
			file, err := os.Create(path)
			require.NoError(t, err)
			zw := zip.NewWriter(file)
			require.NoError(t, addZipBytes(zw, testfixture.LinuxExecutable(machine), "main", 0o755))
			require.NoError(t, addZipBytes(zw, testCAPEM(t), "certs/eventbus-ca.pem", 0o644))
			require.NoError(t, zw.Close())
			require.NoError(t, file.Close())
			require.NoError(t, ValidateSCFPackageZip(path))
		})
	}
}

func TestValidateSCFPackageZipRequiresMain(t *testing.T) {
	require.ErrorContains(t, ValidateSCFPackageZip(writeTestPackage(t, map[string][]byte{"certs/eventbus-ca.pem": testCAPEM(t)})), "main")
}

func TestValidateEventBusCAPEMRejectsExpiredAndFutureCA(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after time.Time
	}{
		{"expired", time.Now().Add(-2 * time.Hour), time.Now().Add(-time.Hour)},
		{"future", time.Now().Add(time.Hour), time.Now().Add(2 * time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pub, private, err := ed25519.GenerateKey(rand.Reader)
			require.NoError(t, err)
			template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: tc.before, NotAfter: tc.after, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
			der, err := x509.CreateCertificate(rand.Reader, template, template, pub, private)
			require.NoError(t, err)
			require.ErrorContains(t, ValidateEventBusCAPEM(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), "validity")
		})
	}
}

func TestValidateEventBusCAPEMRequiresCertificateSigningUsage(t *testing.T) {
	for _, usage := range []x509.KeyUsage{x509.KeyUsageDigitalSignature} {
		pub, private, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: usage}
		der, err := x509.CreateCertificate(rand.Reader, template, template, pub, private)
		require.NoError(t, err)
		require.ErrorContains(t, ValidateEventBusCAPEM(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), "certificate signing")
	}
}

func TestValidateEventBusCAPEMAllowsAbsentKeyUsage(t *testing.T) {
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, private)
	require.NoError(t, err)
	require.NoError(t, ValidateEventBusCAPEM(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})))
}

func testCAPEM(t *testing.T) []byte {
	t.Helper()
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test EventBus CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, private)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func writeTestPackage(t *testing.T, files map[string][]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "package.zip")
	file, err := os.Create(path)
	require.NoError(t, err)
	zw := zip.NewWriter(file)
	for name, content := range files {
		mode := os.FileMode(0o644)
		if name == "main" {
			mode = 0o755
		}
		require.NoError(t, addZipBytes(zw, content, name, mode))
	}
	require.NoError(t, zw.Close())
	require.NoError(t, file.Close())
	return path
}
