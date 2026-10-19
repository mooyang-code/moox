package command

import (
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

	"github.com/mooyang-code/moox/modules/cli/internal/setup/tlstrust"
	"github.com/mooyang-code/moox/modules/cli/internal/testfixture"
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

	// 公网地址走公共证书：本机不需要信任内部 CA，摘要里不出现。
	require.Nil(t, describeSetupBrowserTrust(snapshot))

	control := snapshot.Manifest.ControlHost()
	control.TLSMode = "internal"
	testfixture.SetHost(&snapshot.Manifest, control)
	want := writeCachedConsoleCA(t, control.Address)

	trust := describeSetupBrowserTrust(snapshot)
	require.NotNil(t, trust)
	require.Equal(t, "trusted", trust.Status)
	require.Equal(t, want, trust.Fingerprint)
	require.Equal(t, tlstrust.CAPath(control.Address), trust.CAPath)
	require.True(t, strings.HasPrefix(trust.CAPath, os.Getenv("HOME")))
	for _, text := range []string{"https://" + control.Address + ":9527", "下载并安装根证书", "AGENT-INSTALL.md", "setup trust-browser"} {
		require.Contains(t, trust.OtherMachines, text)
	}
}

func TestTLSTrustFingerprintRejectsMissingOrInvalidFiles(t *testing.T) {
	dir := t.TempDir()
	require.Empty(t, tlstrust.Fingerprint(filepath.Join(dir, "missing.crt")))
	bad := filepath.Join(dir, "bad.crt")
	require.NoError(t, os.WriteFile(bad, []byte("not pem"), 0o600))
	require.Empty(t, tlstrust.Fingerprint(bad))
}
