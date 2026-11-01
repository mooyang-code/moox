package command

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/tlstrust"
	"github.com/stretchr/testify/require"
)

// 写一张测试根证书到初始化时缓存的位置，返回它的 SHA-256 指纹。
func writeCachedConsoleCA(t *testing.T, address string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test Caddy Root"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	require.NoError(t, tlstrust.FileCAStore{}.Save(address, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})))
	sum := sha256.Sum256(der)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}

func TestSetupInitSummaryReportsBrowserTrustForInternalCA(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	snapshot := setupSnapshot(t)
	control := snapshot.Manifest.ControlHost()
	want := writeCachedConsoleCA(t, control.Address)

	trust := describeSetupBrowserTrust(snapshot)
	require.NotNil(t, trust)
	require.Equal(t, "pending", trust.Status, "信任要用户自己做")
	require.Contains(t, trust.Note, "setup trust-browser")
	require.Equal(t, want, trust.Fingerprint)
	require.Equal(t, tlstrust.CAPath(control.Address), trust.CAPath)
	require.True(t, strings.HasPrefix(trust.CAPath, os.Getenv("HOME")))
	for _, text := range []string{"https://" + control.Address + ":9527", "下载并安装根证书", "AGENT-INSTALL.md", "setup trust-host", "setup trust-browser", "不要不加检查地运行包内脚本"} {
		require.Contains(t, trust.OtherMachines, text)
	}
	require.NotContains(t, trust.OtherMachines, "直接运行包内")
}

func TestTLSTrustFingerprintRejectsMissingOrInvalidFiles(t *testing.T) {
	dir := t.TempDir()
	require.Empty(t, tlstrust.Fingerprint(filepath.Join(dir, "missing.crt")))
	bad := filepath.Join(dir, "bad.crt")
	require.NoError(t, os.WriteFile(bad, []byte("not pem"), 0o600))
	require.Empty(t, tlstrust.Fingerprint(bad))
}

func TestSetupBrowserTrustWithoutTerminalPrintsManualCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	refresh, terminal := refreshSetupConsoleCA, setupHasTerminal
	t.Cleanup(func() { refreshSetupConsoleCA, setupHasTerminal = refresh, terminal })
	refreshSetupConsoleCA = func(context.Context, setupconfig.Manifest) error { return nil }
	setupHasTerminal = func() bool { return false }
	snapshot := setupSnapshot(t)
	err := ensureSetupBrowserCATrust(context.Background(), snapshot)
	require.ErrorContains(t, err, "browser_trust_manual")
	require.ErrorContains(t, err, tlstrust.CAPath(snapshot.Manifest.ControlHost().Address))
}
