package gatewayauth

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	key, err := GenerateSigningKey()
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func public(key ed25519.PrivateKey) ed25519.PublicKey { return key.Public().(ed25519.PublicKey) }

// fixture 是两台主机加一个非主机身份的校验表。
type fixture struct {
	storage, factor, cli ed25519.PrivateKey
	keys                 *Keys
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	f := fixture{storage: newKey(t), factor: newKey(t), cli: newKey(t)}
	keys, err := NewKeys(map[string]HostKey{
		"storage": {PublicKey: public(f.storage), Callers: []string{"host-gateway", "host-agent", "storage-primary"}},
		"factor":  {PublicKey: public(f.factor), Callers: []string{"host-gateway", "host-agent", "factor-engine"}},
	}, map[string]ed25519.PublicKey{"moox-cli": public(f.cli)})
	if err != nil {
		t.Fatal(err)
	}
	f.keys = keys
	return f
}

var request = Request{TargetNode: "overseas", Callee: "trpc.moox.trade.Trade", Func: "SubmitOrder", Body: []byte(`{"work":true}`)}

func TestSignAndVerify(t *testing.T) {
	f := newFixture(t)
	now := time.Unix(1_700_000_000, 0)
	for _, credentials := range []Credentials{
		{Caller: "factor-engine", Host: "factor", Key: f.factor},
		{Caller: "moox-cli", Key: f.cli},
	} {
		header, err := Sign(credentials, request, now)
		if err != nil {
			t.Fatal(err)
		}
		if len(header) != 6 || header.Get("X-Moox-Key-Id") != "" {
			t.Fatalf("签名头应为 6 个且不含 KeyID：%v", header)
		}
		claims, err := f.keys.Verify(request, header, now)
		if err != nil {
			t.Fatalf("%s: %v", credentials.Caller, err)
		}
		if claims.Caller != credentials.Caller || claims.Host != credentials.Host || claims.TargetNode != "overseas" ||
			claims.Timestamp != now.Unix() || claims.Nonce != header.Get("X-Moox-Nonce") || claims.TTL != Expire+ClockSkew {
			t.Fatalf("claims = %+v", claims)
		}
	}
}

func TestSignUsesDocumentedMaterial(t *testing.T) {
	key := newKey(t)
	now := time.Unix(1_700_000_000, 0)
	header, err := Sign(Credentials{Caller: "trade", Host: "overseas", Key: key}, request, now)
	if err != nil {
		t.Fatal(err)
	}
	bodyHash := sha256.Sum256(request.Body)
	material := strings.Join([]string{
		"moox-gateway-auth-v2", "trade", "overseas", request.Callee, request.Func,
		hex.EncodeToString(bodyHash[:]), strconv.FormatInt(now.Unix(), 10), header.Get("X-Moox-Nonce"), request.TargetNode,
	}, "\n")
	signature, err := hex.DecodeString(header.Get("X-Moox-Signature"))
	if err != nil || !ed25519.Verify(public(key), []byte(material), signature) {
		t.Fatal("签名材料与文档不一致")
	}
}

func TestVerifyRejectsTamperedRequest(t *testing.T) {
	f := newFixture(t)
	now := time.Unix(1_700_000_000, 0)
	header, err := Sign(Credentials{Caller: "factor-engine", Host: "factor", Key: f.factor}, request, now)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Request, http.Header){
		"请求体":   func(r *Request, _ http.Header) { r.Body = []byte(`{"work":false}`) },
		"被调服务":  func(r *Request, _ http.Header) { r.Callee = "trpc.moox.trade.Other" },
		"被调方法":  func(r *Request, _ http.Header) { r.Func = "CancelOrder" },
		"目标主机":  func(r *Request, _ http.Header) { r.TargetNode = "control" },
		"时间":    func(_ *Request, h http.Header) { h.Set("X-Moox-Timestamp", strconv.FormatInt(now.Unix()+1, 10)) },
		"nonce": func(_ *Request, h http.Header) { h.Set("X-Moox-Nonce", strings.Repeat("0", 64)) },
		"调用方":   func(_ *Request, h http.Header) { h.Set("X-Moox-Caller", "host-agent") },
		"重复的头":  func(_ *Request, h http.Header) { h.Add("X-Moox-Signature", h.Get("X-Moox-Signature")) },
		"缺签名":   func(_ *Request, h http.Header) { h.Del("X-Moox-Signature") },
	} {
		tampered, headers := request, header.Clone()
		mutate(&tampered, headers)
		if _, err := f.keys.Verify(tampered, headers, now); err == nil {
			t.Fatalf("改了%s之后仍然通过", name)
		}
	}
}

func TestVerifyTimeWindow(t *testing.T) {
	f := newFixture(t)
	signedAt := time.Unix(1_700_000_000, 0)
	header, err := Sign(Credentials{Caller: "moox-cli", Key: f.cli}, request, signedAt)
	if err != nil {
		t.Fatal(err)
	}
	for offset, ok := range map[time.Duration]bool{
		-ClockSkew: true, -ClockSkew - time.Second: false, 0: true, Expire + ClockSkew: true, Expire + ClockSkew + time.Second: false,
	} {
		_, err := f.keys.Verify(request, header, signedAt.Add(offset))
		if (err == nil) != ok {
			t.Fatalf("偏移 %s：err=%v，期望通过=%t", offset, err, ok)
		}
	}
}

// 别的主机的密钥不能冒充：因子机的私钥既不能自称存储机，也不能自称因子机上没有部署的组件，更不能冒充 moox-cli。
func TestHostKeyCannotImpersonate(t *testing.T) {
	f := newFixture(t)
	now := time.Unix(1_700_000_000, 0)
	for name, credentials := range map[string]Credentials{
		"自称另一台主机":      {Caller: "storage-primary", Host: "storage", Key: f.factor},
		"组件不在签名主机上":    {Caller: "storage-primary", Host: "factor", Key: f.factor},
		"主机密钥冒充非主机身份":  {Caller: "moox-cli", Key: f.factor},
		"主机密钥带主机头自称身份": {Caller: "moox-cli", Host: "factor", Key: f.factor},
		"身份密钥冒充组件":     {Caller: "factor-engine", Host: "factor", Key: f.cli},
		"组件不带签名主机":     {Caller: "factor-engine", Key: f.factor},
		"没登记的主机":       {Caller: "host-gateway", Host: "unknown", Key: f.factor},
	} {
		header, err := Sign(credentials, request, now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.keys.Verify(request, header, now); err == nil {
			t.Fatalf("%s：应当被拒", name)
		}
	}
}

// 换密钥没有新旧并存：校验表换成新公钥后，旧私钥的签名立即失效。
func TestRotatedKeyInvalidatesOldSignatures(t *testing.T) {
	f := newFixture(t)
	now := time.Unix(1_700_000_000, 0)
	old := Credentials{Caller: "factor-engine", Host: "factor", Key: f.factor}
	header, err := Sign(old, request, now)
	if err != nil {
		t.Fatal(err)
	}
	replacement := newKey(t)
	rotated, err := NewKeys(map[string]HostKey{"factor": {PublicKey: public(replacement), Callers: []string{"factor-engine"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rotated.Verify(request, header, now); err == nil {
		t.Fatal("旧私钥的签名在换密钥之后仍然通过")
	}
	fresh, err := Sign(Credentials{Caller: "factor-engine", Host: "factor", Key: replacement}, request, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rotated.Verify(request, fresh, now); err != nil {
		t.Fatal(err)
	}
}

func TestNewKeysRejectsIdentityNamedLikeHostCaller(t *testing.T) {
	key := newKey(t)
	if _, err := NewKeys(map[string]HostKey{"overseas": {PublicKey: public(key), Callers: []string{"trade"}}},
		map[string]ed25519.PublicKey{"trade": public(key)}); err == nil {
		t.Fatal("非主机身份与主机上的调用方重名时应当拒绝")
	}
	if _, err := NewKeys(map[string]HostKey{"overseas": {PublicKey: public(key)[:31]}}, nil); err == nil {
		t.Fatal("公钥长度不对时应当拒绝")
	}
}

func TestReplayRejectsRepeatedNonceAndPreStartSignatures(t *testing.T) {
	f := newFixture(t)
	started := time.Unix(1_700_000_000, 500_000_000)
	replay := NewReplay(started)
	sign := func(at time.Time) Claims {
		header, err := Sign(Credentials{Caller: "moox-cli", Key: f.cli}, request, at)
		if err != nil {
			t.Fatal(err)
		}
		claims, err := f.keys.Verify(request, header, started.Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		return claims
	}
	now := started.Add(time.Second)
	// 签名时间在启动之前（含同一秒里更早的时刻）：进程不知道它是否已经用过，拒绝。
	for _, at := range []time.Time{started.Add(-10 * time.Second), time.Unix(started.Unix(), 0)} {
		if err := replay.Accept(sign(at), now); err == nil {
			t.Fatalf("签名时间 %s 早于启动时间，应当被拒", at)
		}
	}
	claims := sign(now)
	if err := replay.Accept(claims, now); err != nil {
		t.Fatal(err)
	}
	if err := replay.Accept(claims, now.Add(time.Second)); err == nil {
		t.Fatal("同一个 nonce 第二次出现应当被拒")
	}
	if err := replay.Accept(sign(now), now); err != nil {
		t.Fatalf("新的 nonce 应当通过：%v", err)
	}
	// 记录过期后被清掉；这时签名本身也已过期，验签那一步就不会放行。
	later := now.Add(claims.TTL + 2*time.Second)
	if err := replay.Accept(Claims{Nonce: "other", Timestamp: later.Unix(), TTL: time.Second}, later); err != nil {
		t.Fatal(err)
	}
	if len(replay.seen) != 1 {
		t.Fatalf("过期的防重放记录没有清掉：%d", len(replay.seen))
	}
}

func TestSigningKeyFile(t *testing.T) {
	key := newKey(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "host.key")
	if err := os.WriteFile(path, []byte(EncodeSigningKey(key)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := ReadSigningKey(path)
	if err != nil || !loaded.Equal(key) {
		t.Fatalf("读回的私钥不一致：%v", err)
	}
	parsed, err := ParsePublicKey(EncodePublicKey(public(key)))
	if err != nil || !parsed.Equal(public(key)) {
		t.Fatalf("公钥编码往返不一致：%v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSigningKey(path); err == nil {
		t.Fatal("别人可读的私钥文件应当拒绝")
	}
	link := filepath.Join(dir, "link.key")
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSigningKey(link); err == nil {
		t.Fatal("符号链接应当拒绝")
	}
	for _, text := range []string{"", "not-base64!", "c2hvcnQ=", strings.Repeat("A", 100)} {
		if _, err := ParseSigningKey(text); err == nil {
			t.Fatalf("不合法的私钥 %q 应当拒绝", text)
		}
	}
	if s := (Credentials{Caller: "moox-cli", Key: key}).String(); strings.Contains(s, EncodeSigningKey(key)) || !strings.Contains(s, "moox-cli") {
		t.Fatalf("格式化输出不应带私钥：%s", s)
	}
}
