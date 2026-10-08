package gatewayauth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadCallerKey(t *testing.T) {
	dir := t.TempDir()
	raw, err := MarshalCallerKey(CallerKey{Caller: "host-gateway@compute-1", KeyID: "host-gateway@compute-1-1", Secret: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "caller-host-gateway.key")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	credentials, err := LoadCallerKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if credentials.Caller != "host-gateway@compute-1" || credentials.KeyID != "host-gateway@compute-1-1" || credentials.Secret != "s3cret" {
		t.Fatalf("%+v", credentials)
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCallerKey(path); err == nil || !strings.Contains(err.Error(), "0600") {
		t.Fatalf("组可读的密钥文件应当被拒绝: %v", err)
	}
	_ = os.Chmod(path, 0o600)

	link := filepath.Join(dir, "link.key")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCallerKey(link); err == nil {
		t.Fatal("符号链接应当被拒绝")
	}

	extra := filepath.Join(dir, "extra.key")
	if err := os.WriteFile(extra, []byte(`{"caller":"a","key_id":"a-1","secret":"s","note":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCallerKey(extra); err == nil {
		t.Fatal("未知字段应当被拒绝")
	}
	missing := filepath.Join(dir, "missing.key")
	if err := os.WriteFile(missing, []byte(`{"caller":"a","key_id":"a-1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCallerKey(missing); err == nil {
		t.Fatal("缺少 secret 应当被拒绝")
	}
}

func TestParseCallerKeyValue(t *testing.T) {
	credentials, err := ParseCallerKeyValue("scf-collector", "scf-collector-2:abc:def")
	if err != nil {
		t.Fatal(err)
	}
	if credentials.KeyID != "scf-collector-2" || credentials.Secret != "abc:def" {
		t.Fatalf("%+v", credentials)
	}
	if _, err := ParseCallerKeyValue("scf-collector", "only-id"); err == nil {
		t.Fatal("缺少分隔符应当报错")
	}
}
