package bootstrap

import (
	"context"
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

	"github.com/mooyang-code/moox/packages/notification"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidatePublicCertificateBundle(t *testing.T) {
	now := time.Date(2026, time.August, 3, 0, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(path, testCertificatePEM(t, now.Add(-time.Hour), now.Add(time.Hour)), 0o644))

	fingerprint, count, notAfter, err := validatePublicCertificateBundle(path, now)
	require.NoError(t, err)
	assert.Len(t, fingerprint, 64)
	assert.Equal(t, 1, count)
	assert.True(t, notAfter.Equal(now.Add(time.Hour)))
}

func TestValidatePublicCertificateBundleRejectsPrivateKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private.pem")
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("not-a-key")}), 0o600))

	_, _, _, err := validatePublicCertificateBundle(path, time.Now())
	require.ErrorContains(t, err, "non-certificate")
}

func TestValidatePublicCertificateBundleRejectsExpiredCertificate(t *testing.T) {
	now := time.Date(2026, time.August, 3, 0, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "expired.pem")
	require.NoError(t, os.WriteFile(path, testCertificatePEM(t, now.Add(-2*time.Hour), now.Add(-time.Hour)), 0o600))

	_, _, _, err := validatePublicCertificateBundle(path, now)
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

type recordingSender struct{ messages []notification.Message }

func (s *recordingSender) Send(_ context.Context, message notification.Message) error {
	s.messages = append(s.messages, message)
	return nil
}

func TestCertificateWatchWarnsBeforeExpiry(t *testing.T) {
	now := time.Date(2026, time.October, 8, 0, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(path, testCertificatePEM(t, now.Add(-time.Hour), now.Add(30*24*time.Hour)), 0o600))
	sender := &recordingSender{}
	watch := certificateWatch{
		Files: []certificateWatchFile{{Name: "moox_ca", Path: path}},
		Hosts: func(context.Context) (map[string]time.Time, error) {
			return map[string]time.Time{
				"control":   now.Add(4 * 365 * 24 * time.Hour),
				"storage":   now.Add(10 * 24 * time.Hour),
				"compute-1": now.Add(-time.Hour),
			}, nil
		},
		Now: func() time.Time { return now }, Sender: sender,
	}
	require.NoError(t, watch.Validate(context.Background()), "临近到期和主机证书的问题只告警，不阻止启动")
	keys := map[string]notification.Severity{}
	for _, message := range sender.messages {
		keys[message.Key] = message.Severity
	}
	assert.Equal(t, notification.SeverityWarning, keys["admin_certificate_watch_moox_ca"])
	assert.Equal(t, notification.SeverityWarning, keys["admin_certificate_watch_host_gateway_storage"])
	assert.Equal(t, notification.SeverityCritical, keys["admin_certificate_watch_host_gateway_compute-1"])
	assert.NotContains(t, keys, "admin_certificate_watch_host_gateway_control")
}
