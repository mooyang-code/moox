package gatewayauth

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIdentityFileRoundTripAndStrictLoading(t *testing.T) {
	skill, scf := newKey(t), newKey(t)
	raw, err := EncodeIdentityFile(map[string]ed25519.PublicKey{"scf-collector": public(scf), "moox-skill": public(skill)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"moox-skill"`) || strings.Index(string(raw), "moox-skill") > strings.Index(string(raw), "scf-collector") {
		t.Fatalf("公钥表应按身份名排序：%s", raw)
	}
	path := filepath.Join(t.TempDir(), "access-verification.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := LoadIdentityFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// 表里的身份用自己的私钥能验过；别人的私钥、带签名主机的写法验不过。
	now := time.Unix(1_700_000_000, 0)
	target := Request{TargetNode: "access@storage", Callee: "trpc.moox.storage.PrimaryStore", Func: "ReadTimeSeriesRows"}
	for credentials, ok := range map[*Credentials]bool{
		{Caller: "moox-skill", Key: skill}:                  true,
		{Caller: "moox-skill", Key: scf}:                    false,
		{Caller: "moox-skill", Host: "storage", Key: skill}: false,
		{Caller: "collector-operator", Key: skill}:          false,
	} {
		header, err := Sign(*credentials, target, now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := keys.Verify(target, header, now); (err == nil) != ok {
			t.Fatalf("%v：err=%v，期望通过=%t", credentials, err, ok)
		}
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadIdentityFile(path); err == nil {
		t.Fatal("别人可写可读的公钥表应当拒绝")
	}
	for name, content := range map[string]string{
		"旧版本":   `{"version":1,"credentials":[]}`,
		"未知字段":  `{"version":2,"identities":[],"extra":1}`,
		"多个文档":  `{"version":2,"identities":[]} {}`,
		"重复身份":  `{"version":2,"identities":[{"identity":"a","public_key":"` + EncodePublicKey(public(skill)) + `"},{"identity":"a","public_key":"` + EncodePublicKey(public(scf)) + `"}]}`,
		"公钥不合法": `{"version":2,"identities":[{"identity":"a","public_key":"bad"}]}`,
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadIdentityFile(path); err == nil {
			t.Fatalf("%s：应当拒绝", name)
		}
	}
	if _, err := EncodeIdentityFile(map[string]ed25519.PublicKey{"a": []byte("short")}); err == nil {
		t.Fatal("公钥长度不对时不生成公钥表")
	}
}
