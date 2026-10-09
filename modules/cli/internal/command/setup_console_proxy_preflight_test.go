package command

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConsoleProxyPreflightMissingStateIsReadOnly(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("python3 is required for deployment preflight")
	}
	root := filepath.Join(t.TempDir(), "uninstalled")
	raw, err := exec.Command(python, "-c", consoleProxyPreflightScript, root).Output()
	if err != nil {
		t.Fatal(err)
	}
	var summary struct {
		DeployRootExists bool    `json:"deploy_root_exists"`
		RootCASHA256     *string `json:"root_ca_sha256"`
		RootKeyExists    bool    `json:"root_key_exists"`
		RootKeyMatches   bool    `json:"root_key_matches"`
		FreeBytes        int64   `json:"free_bytes"`
	}
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.DeployRootExists || summary.RootCASHA256 != nil || summary.RootKeyExists || summary.RootKeyMatches || summary.FreeBytes <= 0 {
		t.Fatalf("incorrect empty-install summary: %+v", summary)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("read-only preflight created the deployment root")
	}
}

func TestConsoleProxyPreflightChecksRealCAWithoutExposingKey(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Fatal("openssl is required for deployment preflight")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	crt := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	root := t.TempDir()
	ca := filepath.Join(root, "data/caddy/caddy/pki/authorities/local")
	published := filepath.Join(root, "certs/caddy")
	for _, dir := range []string{ca, published} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	sum := sha256.Sum256(der)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	want := strings.Join(parts, ":")
	for path, content := range map[string][]byte{
		filepath.Join(ca, "root.crt"): crt, filepath.Join(ca, "root.key"): keyPEM,
		filepath.Join(published, "root.crt"): crt, filepath.Join(published, "root.sha256"): []byte(want + "\n"),
		filepath.Join(root, "data/caddy/internal-ca.sha256"): []byte(strings.ToLower(strings.ReplaceAll(want, ":", "")) + "\n"),
	} {
		if err := os.WriteFile(path, content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := exec.Command("python3", "-c", consoleProxyPreflightScript, root).Output()
	if err != nil {
		t.Fatal(err)
	}
	var summary struct {
		RootCASHA256          string `json:"root_ca_sha256"`
		PublishedCASHA256     string `json:"published_ca_sha256"`
		PublishedFingerprint  string `json:"published_fingerprint"`
		PersistentFingerprint string `json:"persistent_fingerprint"`
		RootKeyMatches        bool   `json:"root_key_matches"`
		RootKeyPrivate        bool   `json:"root_key_private"`
	}
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.RootCASHA256 != want || summary.PublishedCASHA256 != want || summary.PublishedFingerprint != want || summary.PersistentFingerprint != want || !summary.RootKeyMatches || !summary.RootKeyPrivate {
		t.Fatal("real CA preflight did not match certificate/key and normalized fingerprints")
	}
	if strings.Contains(string(raw), "PRIVATE KEY") || strings.Contains(string(raw), string(keyPEM)) {
		t.Fatal("preflight exposed private key material")
	}
	if _, err := os.Stat(filepath.Join(ca, "intermediate.crt")); !os.IsNotExist(err) {
		t.Fatal("read-only preflight provisioned a missing intermediate")
	}
}
