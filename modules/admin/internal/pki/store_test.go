package pki

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(privateFixtureDir(t))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}

func privateFixtureDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	require.NoError(t, os.Mkdir(dir, 0o700))
	return dir
}

func readFixtureFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	return raw
}

func parseFixtureCertificate(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(readFixtureFile(t, path))
	require.NotNil(t, block)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	return cert
}

func requirePrivateMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	require.NoError(t, err)
	require.Equal(t, mode, info.Mode().Perm())
	require.Zero(t, info.Mode()&os.ModeSymlink)
}

func TestPersistentCAReuseIssuanceAndPrivateTrust(t *testing.T) {
	store := newStore(t)
	info, err := store.EnsureCA()
	require.NoError(t, err)
	require.True(t, info.Created)
	now := time.Now().UTC()
	require.WithinDuration(t, now.AddDate(10, 0, 0), info.NotAfter, 5*time.Second)
	root := store.root.Name()
	keyBefore := readFixtureFile(t, filepath.Join(root, "ca.key"))
	certBefore := readFixtureFile(t, filepath.Join(root, "ca.crt"))
	other, err := Open(root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, other.Close()) })
	again, err := other.EnsureCA()
	require.NoError(t, err)
	require.False(t, again.Created)
	require.Equal(t, info.CertificateInfo, again.CertificateInfo)
	require.True(t, bytes.Equal(keyBefore, readFixtureFile(t, filepath.Join(root, "ca.key"))), "CA private key must remain unchanged")
	require.True(t, bytes.Equal(certBefore, readFixtureFile(t, filepath.Join(root, "ca.crt"))))
	parent := privateFixtureDir(t)
	host := HostIdentity{HostID: "storage", Address: "192.0.2.20", PrivateAddress: "storage.internal.example.test"}
	issued, err := store.Issue(host, filepath.Join(parent, "first"))
	require.NoError(t, err)
	require.Equal(t, info.SHA256, issued.CAFingerprint)
	require.WithinDuration(t, now.AddDate(5, 0, 0), issued.NotAfter, 5*time.Second)
	leaf := parseFixtureCertificate(t, issued.Certificate)
	ca := parseFixtureCertificate(t, filepath.Join(root, "ca.crt"))
	require.True(t, ca.IsCA)
	require.True(t, ca.MaxPathLenZero)
	require.False(t, leaf.IsCA)
	require.Equal(t, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, leaf.ExtKeyUsage)
	require.Zero(t, leaf.KeyUsage&x509.KeyUsageCertSign)
	for _, identity := range []string{host.HostID, host.Address, host.PrivateAddress} {
		require.NoError(t, leaf.VerifyHostname(identity))
	}
	require.Error(t, leaf.VerifyHostname("other-host"))
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(certBefore))
	_, err = leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: host.HostID})
	require.NoError(t, err)
	pair, err := tls.LoadX509KeyPair(issued.Certificate, issued.Key)
	require.NoError(t, err)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(server.Close)
	wrongCA := newStore(t)
	_, err = wrongCA.EnsureCA()
	require.NoError(t, err)
	wrongPool := x509.NewCertPool()
	require.True(t, wrongPool.AppendCertsFromPEM(readFixtureFile(t, filepath.Join(wrongCA.root.Name(), "ca.crt"))))
	for _, tc := range []struct {
		name string
		pool *x509.CertPool
		sni  string
		pass bool
	}{
		{"private root and host ID", pool, host.HostID, true},
		{"private root and public IP", pool, host.Address, true},
		{"private root and private DNS", pool, host.PrivateAddress, true},
		{"wrong root", wrongPool, host.HostID, false},
		{"wrong SNI", pool, "other-host", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: tc.pool, ServerName: tc.sni, MinVersion: tls.VersionTLS12}}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
			response, err := client.Get(server.URL)
			if !tc.pass {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			defer response.Body.Close()
			require.Equal(t, http.StatusNoContent, response.StatusCode)
		})
	}
	second, err := other.Issue(host, filepath.Join(parent, "second"))
	require.NoError(t, err)
	require.NotEqual(t, issued.Serial, second.Serial)
	require.NotEqual(t, issued.SHA256, second.SHA256)
	secondPair, err := tls.LoadX509KeyPair(second.Certificate, second.Key)
	require.NoError(t, err)
	require.False(t, pair.PrivateKey.(*ecdsa.PrivateKey).PublicKey.Equal(&secondPair.PrivateKey.(*ecdsa.PrivateKey).PublicKey))
	require.True(t, bytes.Equal(readFixtureFile(t, second.Certificate), readFixtureFile(t, filepath.Join(root, "hosts", "storage.crt"))))
	files, err := os.ReadDir(filepath.Join(root, "hosts"))
	require.NoError(t, err)
	require.Len(t, files, 1, "inventory must contain only the latest public certificate")
	exportDir := filepath.Join(parent, "export")
	exported, err := store.ExportCA(exportDir)
	require.NoError(t, err)
	require.Equal(t, info.CertificateInfo, exported)
	files, err = os.ReadDir(exportDir)
	require.NoError(t, err)
	require.Len(t, files, 1)
	require.Equal(t, "moox-ca.crt", files[0].Name())
	require.True(t, bytes.Equal(certBefore, readFixtureFile(t, filepath.Join(exportDir, "moox-ca.crt"))))
	for _, dir := range []string{root, filepath.Join(root, "hosts"), filepath.Dir(issued.Key), exportDir} {
		requirePrivateMode(t, dir, 0o700)
	}
	for _, file := range []string{filepath.Join(root, "ca.key"), filepath.Join(root, "ca.crt"), issued.Key, issued.Certificate, issued.CA, filepath.Join(root, "hosts", "storage.crt")} {
		requirePrivateMode(t, file, 0o600)
	}
	metadata, err := json.Marshal([]any{info, issued, exported, store})
	require.NoError(t, err)
	require.NotContains(t, string(metadata), "BEGIN")
}

func TestConcurrentEnsureKeepsOneRootAcrossIndependentStores(t *testing.T) {
	dir := privateFixtureDir(t)
	const writers = 6
	stores := make([]*Store, writers)
	for i := range stores {
		store, err := Open(dir)
		require.NoError(t, err)
		stores[i] = store
		t.Cleanup(func() { require.NoError(t, store.Close()) })
	}
	var wait sync.WaitGroup
	infos, errs := make([]CAInfo, writers), make([]error, writers)
	start := make(chan struct{})
	for i, store := range stores {
		wait.Go(func() {
			<-start
			infos[i], errs[i] = store.EnsureCA()
		})
	}
	close(start)
	wait.Wait()
	created := 0
	for i, err := range errs {
		require.NoError(t, err)
		require.Equal(t, infos[0].CertificateInfo, infos[i].CertificateInfo)
		if infos[i].Created {
			created++
		}
	}
	require.Equal(t, 1, created)
}

func TestInvalidOrPartialCANeverRegeneratesTrust(t *testing.T) {
	for _, mutation := range []string{"missing certificate", "missing key", "missing pair", "corrupt continuity marker", "corrupt certificate", "corrupt key", "mismatched key", "loose key", "symlink key", "expired", "not yet valid", "not a CA"} {
		t.Run(mutation, func(t *testing.T) {
			store := newStore(t)
			_, err := store.EnsureCA()
			require.NoError(t, err)
			root := store.root.Name()
			certPath, keyPath := filepath.Join(root, "ca.crt"), filepath.Join(root, "ca.key")
			switch mutation {
			case "missing certificate":
				require.NoError(t, os.Remove(certPath))
			case "missing key":
				require.NoError(t, os.Remove(keyPath))
			case "missing pair":
				require.NoError(t, os.Remove(keyPath))
				require.NoError(t, os.Remove(certPath))
			case "corrupt continuity marker":
				require.NoError(t, os.WriteFile(filepath.Join(root, "ca.sha256"), []byte("invalid fingerprint fixture"), 0o600))
			case "corrupt certificate":
				require.NoError(t, os.WriteFile(certPath, []byte("invalid certificate fixture"), 0o600))
			case "corrupt key":
				require.NoError(t, os.WriteFile(keyPath, []byte("invalid key fixture"), 0o600))
			case "mismatched key":
				other := newStore(t)
				_, err = other.EnsureCA()
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(keyPath, readFixtureFile(t, filepath.Join(other.root.Name(), "ca.key")), 0o600))
			case "loose key":
				require.NoError(t, os.Chmod(keyPath, 0o644))
			case "symlink key":
				require.NoError(t, os.Rename(keyPath, keyPath+".original"))
				require.NoError(t, os.Symlink(keyPath+".original", keyPath))
			default:
				ca, key, _, err := store.loadCA()
				require.NoError(t, err)
				switch mutation {
				case "expired":
					ca.NotBefore, ca.NotAfter = time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour)
				case "not yet valid":
					ca.NotBefore = time.Now().Add(24 * time.Hour)
				case "not a CA":
					ca.IsCA, ca.MaxPathLenZero = false, false
					ca.MaxPathLen, ca.KeyUsage = -1, x509.KeyUsageDigitalSignature
				}
				der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
			}
			beforeCert, _ := os.ReadFile(certPath)
			beforeKey, _ := os.ReadFile(keyPath)
			_, err = store.EnsureCA()
			require.ErrorIs(t, err, ErrInvalidCA)
			output := filepath.Join(t.TempDir(), "not-published")
			_, err = store.Issue(HostIdentity{HostID: "storage", Address: "192.0.2.20"}, output)
			require.ErrorIs(t, err, ErrInvalidCA)
			_, err = store.ExportCA(output)
			require.ErrorIs(t, err, ErrInvalidCA)
			_, err = os.Lstat(output)
			require.True(t, os.IsNotExist(err))
			afterCert, _ := os.ReadFile(certPath)
			afterKey, _ := os.ReadFile(keyPath)
			require.True(t, bytes.Equal(beforeCert, afterCert), "invalid original CA certificate must not be replaced")
			require.True(t, bytes.Equal(beforeKey, afterKey), "invalid original CA key must not be replaced")
		})
	}
}

func TestIssueBoundsLeafByCAExpiryAndSupportsIPv6(t *testing.T) {
	store := newStore(t)
	first, err := store.EnsureCA()
	require.NoError(t, err)
	store.now = func() time.Time { return first.NotAfter.AddDate(-2, 0, 0) }
	issued, err := store.Issue(HostIdentity{HostID: "compute-1", Address: "2001:db8::1", PrivateAddress: "fd00::1"}, filepath.Join(privateFixtureDir(t), "ipv6"))
	require.NoError(t, err)
	require.Equal(t, first.NotAfter, issued.NotAfter)
	cert := parseFixtureCertificate(t, issued.Certificate)
	require.NoError(t, cert.VerifyHostname("2001:db8::1"))
	require.NoError(t, cert.VerifyHostname("fd00::1"))
}

func TestRejectUnsafeDirectoriesNamesAndExistingBundles(t *testing.T) {
	store := newStore(t)
	_, err := store.EnsureCA()
	require.NoError(t, err)
	parent := privateFixtureDir(t)
	output := filepath.Join(parent, "bundle")
	host := HostIdentity{HostID: "control", Address: "control.example.test"}
	first, err := store.Issue(host, output)
	require.NoError(t, err)
	key := readFixtureFile(t, first.Key)
	_, err = store.Issue(host, output)
	require.ErrorContains(t, err, "already exist")
	require.True(t, bytes.Equal(key, readFixtureFile(t, first.Key)), "live bundle must never be overwritten")
	for _, invalid := range []HostIdentity{
		{HostID: "../outside", Address: "192.0.2.1"}, {HostID: "control", Address: "https://host"},
		{HostID: "control", Address: "host:11003"}, {HostID: "control", Address: "192.0.2.1", PrivateAddress: "*"},
	} {
		_, err = store.Issue(invalid, filepath.Join(parent, "invalid"))
		require.Error(t, err)
	}
	_, err = os.Stat(filepath.Join(parent, "invalid"))
	require.True(t, os.IsNotExist(err))
	loose := filepath.Join(parent, "loose")
	require.NoError(t, os.Mkdir(loose, 0o755))
	_, err = Open(loose)
	require.ErrorContains(t, err, "0700")
	link := filepath.Join(parent, "root-link")
	require.NoError(t, os.Symlink(store.root.Name(), link))
	_, err = Open(link)
	require.Error(t, err)
	export := filepath.Join(parent, "export")
	require.NoError(t, os.Mkdir(export, 0o700))
	outside := filepath.Join(parent, "outside")
	require.NoError(t, os.WriteFile(outside, []byte("public fixture unchanged"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(export, "moox-ca.crt")))
	_, err = store.ExportCA(export)
	require.Error(t, err)
	require.Equal(t, "public fixture unchanged", string(readFixtureFile(t, outside)))
	files, err := os.ReadDir(parent)
	require.NoError(t, err)
	for _, file := range files {
		require.NotContains(t, file.Name(), ".issue-", "staging directories must be cleaned")
	}
}
