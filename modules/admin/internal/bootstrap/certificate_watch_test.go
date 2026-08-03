package bootstrap

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidatePublicCertificateBundle(t *testing.T) {
	now := time.Date(2026, time.August, 3, 0, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(path, testCertificatePEM(t, now.Add(-time.Hour), now.Add(time.Hour)), 0o644))

	fingerprint, count, err := validatePublicCertificateBundle(path, now)
	require.NoError(t, err)
	assert.Len(t, fingerprint, 64)
	assert.Equal(t, 1, count)
}

func TestValidatePublicCertificateBundleRejectsPrivateKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private.pem")
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("not-a-key")}), 0o600))

	_, _, err := validatePublicCertificateBundle(path, time.Now())
	require.ErrorContains(t, err, "non-certificate")
}

func TestValidatePublicCertificateBundleRejectsExpiredCertificate(t *testing.T) {
	now := time.Date(2026, time.August, 3, 0, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "expired.pem")
	require.NoError(t, os.WriteFile(path, testCertificatePEM(t, now.Add(-2*time.Hour), now.Add(-time.Hour)), 0o600))

	_, _, err := validatePublicCertificateBundle(path, now)
	require.ErrorContains(t, err, "expired")
}

func testCertificatePEM(t *testing.T, notBefore, notAfter time.Time) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "moox-test-ca"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		IsCA:         true,
		KeyUsage:     x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
