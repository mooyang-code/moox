package engine

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestOfflineInitializationUsesOnlyPKIAndNeverReplacesState(t *testing.T) {
	c := testConfig(t, "http://127.0.0.1:11000", "http://127.0.0.1:9528")
	// Both configured listeners are occupied: offline PKI must not bind them.
	for _, address := range []string{net.JoinHostPort(c.Public.Bind, strconv.Itoa(c.Public.Port)), c.Health.Listen} {
		listener, err := net.Listen("tcp", address)
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
	}
	ca, err := InitializeState(context.Background(), c, false)
	if err != nil || ca == "" {
		t.Fatalf("offline initialization failed: %v", err)
	}
	if _, err := InitializeState(context.Background(), c, false); err == nil {
		t.Fatal("creation accepted an existing CA")
	}
	if _, err := os.Stat(filepath.Join(c.TLS.StorageRoot, "certificates")); !os.IsNotExist(err) {
		t.Fatal("offline initialization issued a site certificate")
	}
	if err := os.Remove(c.TLS.CABaseline); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareCA(c); err == nil {
		t.Fatal("serving imported a legacy baseline implicitly")
	}
	imported, err := InitializeState(context.Background(), c, true)
	if err != nil || imported != ca {
		t.Fatalf("explicit trusted import changed CA: %v", err)
	}
	if err := os.Remove(c.TLS.CABaseline); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(c.TLS.CAPublishDir, "root.sha256")); err != nil {
		t.Fatal(err)
	}
	if _, err := InitializeState(context.Background(), c, true); err == nil {
		t.Fatal("legacy import accepted missing original published fingerprint")
	}
	if _, err := os.Stat(c.TLS.CABaseline); !os.IsNotExist(err) {
		t.Fatal("rejected import wrote a new trust baseline")
	}
}

func TestCAStateRefusesAdditionalCertificatesInTrustFiles(t *testing.T) {
	c := testConfig(t, "http://127.0.0.1:11000", "http://127.0.0.1:9528")
	if _, err := InitializeState(context.Background(), c, false); err != nil {
		t.Fatal(err)
	}
	rootFile := filepath.Join(caDir(c), "root.crt")
	publishedFile := filepath.Join(c.TLS.CAPublishDir, "root.crt")
	root, err := os.ReadFile(rootFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{publishedFile, rootFile} {
		if err := os.WriteFile(file, append(append([]byte(nil), root...), root...), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := CheckImportState(c); err == nil {
			t.Fatal("import accepted an additional PEM certificate in a trust file")
		}
		if err := os.WriteFile(file, root, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
