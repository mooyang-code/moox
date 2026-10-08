package pki

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEnsureCAIsReused(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pki")
	now := time.Now()
	created, first, err := EnsureCA(dir, now)
	require.NoError(t, err)
	require.True(t, created)
	require.True(t, first.IsCA)
	require.WithinDuration(t, now.Add(CAValidity), first.NotAfter, time.Minute)
	for _, name := range []string{CAKeyFile, CACertFile} {
		info, err := os.Stat(filepath.Join(dir, name))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
	created, second, err := EnsureCA(dir, now.Add(time.Hour))
	require.NoError(t, err)
	require.False(t, created, "重复执行复用已有 CA")
	require.Equal(t, first.SerialNumber, second.SerialNumber)

	require.NoError(t, os.Remove(filepath.Join(dir, CACertFile)))
	_, _, err = EnsureCA(dir, now)
	require.Error(t, err, "只剩私钥时不能悄悄重建 CA")
}

func TestIssueHostCertificateVerifiesAgainstCA(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pki")
	now := time.Now()
	_, ca, err := EnsureCA(dir, now)
	require.NoError(t, err)
	issued, err := IssueHostCertificate(dir, "storage", []string{"146.56.196.204", "10.206.0.5"}, now)
	require.NoError(t, err)
	require.Contains(t, issued.Cert.DNSNames, "storage")
	require.Len(t, issued.Cert.IPAddresses, 2)
	require.WithinDuration(t, now.Add(HostValidity), issued.Cert.NotAfter, time.Minute)

	roots := x509.NewCertPool()
	roots.AddCert(ca)
	_, err = issued.Cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: "storage", KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	require.NoError(t, err)
	require.True(t, issued.Cert.IPAddresses[0].Equal(net.ParseIP("146.56.196.204")))
	_, err = tls.X509KeyPair(issued.CertPEM, issued.KeyPEM)
	require.NoError(t, err)

	otherDir := filepath.Join(t.TempDir(), "other")
	_, other, err := EnsureCA(otherDir, now)
	require.NoError(t, err)
	otherRoots := x509.NewCertPool()
	otherRoots.AddCert(other)
	_, err = issued.Cert.Verify(x509.VerifyOptions{Roots: otherRoots, DNSName: "storage"})
	require.Error(t, err, "其他 CA 不能校验通过")
}

func TestIssueHostCertificateRequiresCA(t *testing.T) {
	_, err := IssueHostCertificate(filepath.Join(t.TempDir(), "missing"), "control", nil, time.Now())
	require.Error(t, err)
}
