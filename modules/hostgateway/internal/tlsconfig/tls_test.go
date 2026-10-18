package tlsconfig

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/hostgateway/internal/testcert"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"github.com/stretchr/testify/require"
)

func TestLoadPrivateIdentity(t *testing.T) {
	ca := testcert.New(t, nil)
	paths := testcert.Files(t, ca, "storage", nil)
	material, err := Load("storage", paths)
	require.NoError(t, err)
	require.Equal(t, "storage", material.HostID())
	digest := sha256.Sum256(ca.Certificate.Raw)
	require.Equal(t, hex.EncodeToString(digest[:]), material.Fingerprint())
	server := material.Server()
	require.Equal(t, uint16(tls.VersionTLS12), server.MinVersion)
	require.Equal(t, tls.NoClientCert, server.ClientAuth)
	require.Nil(t, server.ClientCAs)
	require.Len(t, server.Certificates, 1)
	for _, name := range []string{"storage", "127.0.0.1", "10.0.0.42"} {
		require.NoError(t, server.Certificates[0].Leaf.VerifyHostname(name))
	}
	client, err := material.Client("control")
	require.NoError(t, err)
	require.Equal(t, "control", client.ServerName)
	require.False(t, client.InsecureSkipVerify)
	require.Equal(t, uint16(tls.VersionTLS12), client.MinVersion)
	require.Len(t, client.RootCAs.Subjects(), 1)
	require.Equal(t, ca.Certificate.RawSubject, client.RootCAs.Subjects()[0])
	require.Empty(t, client.Certificates)
	for _, invalid := range []string{"", "CONTROL", "control ", "127.0.0.1", "control/else"} {
		_, err := material.Client(invalid)
		require.Error(t, err)
	}
	// A host named "root" is a TLS identity, never an OS trust-store sentinel.
	rootClient, err := material.Client("root")
	require.NoError(t, err)
	require.Len(t, rootClient.RootCAs.Subjects(), 1)
	// Every client receives a separate trust pool. Modifying it cannot change
	// the material or a previously created client.
	client.RootCAs.AddCert(testcert.New(t, nil).Certificate)
	again, err := material.Client("control")
	require.NoError(t, err)
	require.Len(t, again.RootCAs.Subjects(), 1)
	// Files are not reopened after validation, so an on-disk replacement cannot
	// silently change the running server's identity.
	testcert.Write(t, paths.KeyFile, []byte("replaced fixture"))
	require.NotNil(t, material.Server().Certificates[0].PrivateKey)
}

func TestRejectInvalidTrustAndHostCertificates(t *testing.T) {
	tests := []struct {
		name       string
		hostID     string
		root, leaf func(*x509.Certificate)
	}{
		{name: "noncanonical identity", hostID: "Storage"},
		{name: "different host", hostID: "compute"},
		{name: "CA expired", root: func(c *x509.Certificate) { c.NotAfter = time.Now().Add(-time.Minute) }},
		{name: "CA not current", root: func(c *x509.Certificate) { c.NotBefore = time.Now().Add(time.Minute) }},
		{name: "CA missing constraints", root: func(c *x509.Certificate) { c.BasicConstraintsValid = false }},
		{name: "CA cannot sign", root: func(c *x509.Certificate) { c.KeyUsage = x509.KeyUsageDigitalSignature }},
		{name: "root is a leaf", root: func(c *x509.Certificate) { c.IsCA = false }},
		{name: "leaf is a CA", leaf: func(c *x509.Certificate) { c.IsCA = true }},
		{name: "leaf expired", leaf: func(c *x509.Certificate) { c.NotAfter = time.Now().Add(-time.Minute) }},
		{name: "leaf not current", leaf: func(c *x509.Certificate) { c.NotBefore = time.Now().Add(time.Minute) }},
		{name: "leaf beyond CA", leaf: func(c *x509.Certificate) { c.NotAfter = time.Now().Add(25 * time.Hour) }},
		{name: "common name only", leaf: func(c *x509.Certificate) { c.DNSNames = nil }},
		{name: "client usage only", leaf: func(c *x509.Certificate) { c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth} }},
		{name: "any usage only", leaf: func(c *x509.Certificate) { c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageAny} }},
		{name: "no explicit usage", leaf: func(c *x509.Certificate) { c.ExtKeyUsage = nil }},
		{name: "no digital signature", leaf: func(c *x509.Certificate) { c.KeyUsage = x509.KeyUsageKeyEncipherment }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ca := testcert.New(t, tt.root)
			paths := testcert.Files(t, ca, "storage", tt.leaf)
			host := tt.hostID
			if host == "" {
				host = "storage"
			}
			_, err := Load(host, paths)
			require.Error(t, err)
		})
	}
	ca := testcert.New(t, nil)
	paths := testcert.Files(t, ca, "storage", nil)
	other := testcert.Files(t, testcert.New(t, nil), "storage", nil)
	t.Run("different issuer", func(t *testing.T) {
		wrong := paths
		wrong.CAFile = other.CAFile
		_, err := Load("storage", wrong)
		require.Error(t, err)
	})
	t.Run("mismatched private key", func(t *testing.T) {
		wrong := paths
		wrong.KeyFile = other.KeyFile
		_, err := Load("storage", wrong)
		require.ErrorContains(t, err, "does not match")
	})
	t.Run("unsigned CA", func(t *testing.T) {
		broken := append([]byte(nil), ca.Certificate.Raw...)
		broken[len(broken)-1] ^= 1
		cert, err := x509.ParseCertificate(broken)
		require.NoError(t, err)
		require.Error(t, cert.CheckSignatureFrom(cert))
		// Corrupting the DER signature leaves PEM structurally valid.
		testcert.Write(t, paths.CAFile, encodeCert(broken))
		_, err = Load("storage", paths)
		require.Error(t, err)
	})
}

func encodeCert(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestRejectMalformedAndUnboundedFiles(t *testing.T) {
	for _, field := range []string{"CA", "certificate", "key"} {
		for _, mode := range []string{"missing", "symlink", "directory", "empty", "garbage", "leading", "trailing", "multiple", "oversize"} {
			t.Run(field+"/"+mode, func(t *testing.T) {
				paths := testcert.Files(t, testcert.New(t, nil), "storage", nil)
				path := paths.CertificateFile
				limit := 1 << 20
				if field == "CA" {
					path = paths.CAFile
				} else if field == "key" {
					path, limit = paths.KeyFile, 64<<10
				}
				original, err := os.ReadFile(path)
				require.NoError(t, err)
				sentinel := []byte("private-test-sentinel-do-not-echo")
				switch mode {
				case "missing":
					require.NoError(t, os.Remove(path))
				case "symlink":
					if runtime.GOOS == "windows" {
						t.Skip("Windows symlink fixture requires developer privilege")
					}
					target := filepath.Join(filepath.Dir(path), "target")
					testcert.Write(t, target, original)
					require.NoError(t, os.Remove(path))
					require.NoError(t, os.Symlink(target, path))
				case "directory":
					require.NoError(t, os.Remove(path))
					require.NoError(t, os.Mkdir(path, 0o700))
				case "empty":
					testcert.Write(t, path, nil)
				case "garbage":
					testcert.Write(t, path, sentinel)
				case "leading":
					testcert.Write(t, path, append(sentinel, original...))
				case "trailing":
					testcert.Write(t, path, append(original, sentinel...))
				case "multiple":
					testcert.Write(t, path, append(original, original...))
				case "oversize":
					testcert.Write(t, path, bytes.Repeat([]byte("x"), limit+1))
				}
				_, err = Load("storage", paths)
				require.Error(t, err)
				require.NotContains(t, err.Error(), string(sentinel))
			})
		}
	}
}

func TestPrivateKeyPermissionsAndExactValidityBoundary(t *testing.T) {
	ca := testcert.New(t, nil)
	paths := testcert.Files(t, ca, "storage", nil)
	if runtime.GOOS != "windows" {
		for _, permission := range []os.FileMode{0o644, 0o660, 0o400} {
			require.NoError(t, os.Chmod(paths.KeyFile, permission))
			_, err := Load("storage", paths)
			require.ErrorContains(t, err, "0600")
		}
		require.NoError(t, os.Chmod(paths.KeyFile, 0o600))
	}
	material, err := Load("storage", paths)
	require.NoError(t, err)
	leaf := material.Server().Certificates[0].Leaf
	_, err = loadAt("storage", paths, leaf.NotAfter)
	require.Error(t, err)
	_, err = loadAt("storage", paths, leaf.NotBefore)
	require.NoError(t, err)
	_, err = loadAt("storage", paths, ca.Certificate.NotAfter)
	require.Error(t, err)
}

func TestMissingPathRejectedWithoutCreatingFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "never-created")
	_, err := Load("storage", hostgatewayconfig.TLS{CAFile: filepath.Join(dir, "ca.crt")})
	require.Error(t, err)
	_, err = os.Stat(dir)
	require.ErrorIs(t, err, os.ErrNotExist)
}
