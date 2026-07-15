package gatewayauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSignAndVerifyValidRequest(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	credentials := Credentials{KeyID: "node-key", Secret: "secret"}
	request := Request{
		Method:     http.MethodPost,
		Path:       "/api/gateway/jobs/a%2Fb?ignored=true",
		TargetNode: "node-7",
		Body:       []byte(`{"work":true}`),
	}

	header, err := Sign(credentials, request, now)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	for _, name := range []string{
		"X-Moox-Key-Id", "X-Moox-Timestamp", "X-Moox-Nonce",
		"X-Moox-Target-Node", "X-Moox-Signature",
	} {
		if header.Get(name) == "" {
			t.Fatalf("header %s is empty", name)
		}
	}
	claims, err := Verify(credentials, request, header, now)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if claims.KeyID != credentials.KeyID || claims.TargetNode != request.TargetNode || claims.Timestamp != now.Unix() {
		t.Fatalf("claims = %+v", claims)
	}
	if claims.Nonce != header.Get("X-Moox-Nonce") || claims.TTL != 90*time.Second {
		t.Fatalf("claims = %+v", claims)
	}
}

func TestSignUsesCanonicalGatewayMaterial(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	credentials := Credentials{KeyID: "key", Secret: "secret"}
	request := Request{Method: "post", Path: "/v1/a%2Fb?query=ignored", TargetNode: "node-a", Body: []byte("body")}
	header, err := Sign(credentials, request, now)
	if err != nil {
		t.Fatal(err)
	}
	bodyHash := sha256.Sum256(request.Body)
	material := strings.Join([]string{
		Version,
		"POST",
		"/v1/a%2Fb",
		hex.EncodeToString(bodyHash[:]),
		strconv.FormatInt(now.Unix(), 10),
		header.Get("X-Moox-Nonce"),
		request.TargetNode,
	}, "\n")
	mac := hmac.New(sha256.New, []byte(credentials.Secret))
	_, _ = mac.Write([]byte(material))
	if got, want := header.Get("X-Moox-Signature"), hex.EncodeToString(mac.Sum(nil)); got != want {
		t.Fatalf("signature = %q, want %q", got, want)
	}
}

func TestVerifyBindsTargetBodyAndPath(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	credentials := Credentials{KeyID: "key", Secret: "secret"}
	request := Request{Method: http.MethodPost, Path: "/v1/run", TargetNode: "node-a", Body: []byte("body")}
	header, err := Sign(credentials, request, now)
	if err != nil {
		t.Fatal(err)
	}

	tests := map[string]Request{
		"target": {Method: request.Method, Path: request.Path, TargetNode: "node-b", Body: request.Body},
		"body":   {Method: request.Method, Path: request.Path, TargetNode: request.TargetNode, Body: []byte("changed")},
		"path":   {Method: request.Method, Path: "/v1/other", TargetNode: request.TargetNode, Body: request.Body},
	}
	for name, changed := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Verify(credentials, changed, header, now); err == nil {
				t.Fatal("changed request accepted")
			}
		})
	}
}

func TestVerifyRejectsExpiredAndFutureTimestamp(t *testing.T) {
	signedAt := time.Unix(1_700_000_000, 0)
	credentials := Credentials{KeyID: "key", Secret: "secret", Expire: time.Minute, ClockSkew: 30 * time.Second}
	request := Request{Method: http.MethodGet, Path: "/readyz", TargetNode: "node-a"}
	header, err := Sign(credentials, request, signedAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(credentials, request, header, signedAt.Add(91*time.Second)); err == nil {
		t.Fatal("expired timestamp accepted")
	}
	if _, err := Verify(credentials, request, header, signedAt.Add(-31*time.Second)); err == nil {
		t.Fatal("future timestamp accepted")
	}
}

func TestVerifyClaimsTTLCoversEntireFutureTimestampWindow(t *testing.T) {
	signedAt := time.Unix(1_700_000_000, 0)
	expire := time.Minute
	skew := 30 * time.Second
	credentials := Credentials{KeyID: "key", Secret: "secret", Expire: expire, ClockSkew: skew}
	request := Request{Method: http.MethodGet, Path: "/readyz", TargetNode: "node-a"}
	header, err := Sign(credentials, request, signedAt)
	if err != nil {
		t.Fatal(err)
	}
	verifiedAt := signedAt.Add(-skew)
	claims, err := Verify(credentials, request, header, verifiedAt)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	want := signedAt.Add(expire).Add(skew).Sub(verifiedAt)
	if claims.TTL != want {
		t.Fatalf("Claims.TTL = %v, want %v", claims.TTL, want)
	}
}

func TestCredentialsValidation(t *testing.T) {
	request := Request{Method: http.MethodGet, Path: "/readyz", TargetNode: "node-a"}
	now := time.Unix(1_700_000_000, 0)
	tests := map[string]Credentials{
		"missing key id":      {Secret: "secret"},
		"missing secret":      {KeyID: "key"},
		"negative expiry":     {KeyID: "key", Secret: "secret", Expire: -time.Second},
		"negative clock skew": {KeyID: "key", Secret: "secret", ClockSkew: -time.Second},
		"whitespace key id":   {KeyID: " ", Secret: "secret"},
		"leading key space":   {KeyID: " key", Secret: "secret"},
		"trailing key space":  {KeyID: "key ", Secret: "secret"},
		"key control":         {KeyID: "key\x00id", Secret: "secret"},
		"key delete control":  {KeyID: "key\x7fid", Secret: "secret"},
		"whitespace secret":   {KeyID: "key", Secret: " "},
	}
	for name, credentials := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Sign(credentials, request, now); err == nil {
				t.Fatal("Sign() accepted invalid credentials")
			}
			if _, err := Verify(credentials, request, http.Header{}, now); err == nil {
				t.Fatal("Verify() accepted invalid credentials")
			}
		})
	}
}

func TestSignRejectsNonCanonicalTargetNode(t *testing.T) {
	credentials := Credentials{KeyID: "key", Secret: "secret"}
	now := time.Unix(1_700_000_000, 0)
	for name, targetNode := range map[string]string{
		"leading space":  " node-a",
		"trailing space": "node-a ",
		"control":        "node\x00a",
		"delete control": "node\x7fa",
	} {
		t.Run(name, func(t *testing.T) {
			request := Request{Method: http.MethodGet, Path: "/readyz", TargetNode: targetNode}
			if _, err := Sign(credentials, request, now); err == nil {
				t.Fatal("Sign() accepted invalid target node")
			}
		})
	}
}

func TestVerifyRejectsMalformedHeaders(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	credentials := Credentials{KeyID: "key", Secret: "secret"}
	request := Request{Method: http.MethodPost, Path: "/v1/run", TargetNode: "node-a"}
	valid, err := Sign(credentials, request, now)
	if err != nil {
		t.Fatal(err)
	}

	tests := map[string]func(http.Header){
		"missing key id":    func(h http.Header) { h.Del("X-Moox-Key-Id") },
		"wrong key id":      func(h http.Header) { h.Set("X-Moox-Key-Id", "other") },
		"bad timestamp":     func(h http.Header) { h.Set("X-Moox-Timestamp", "nope") },
		"missing nonce":     func(h http.Header) { h.Del("X-Moox-Nonce") },
		"bad nonce":         func(h http.Header) { h.Set("X-Moox-Nonce", "abc") },
		"missing target":    func(h http.Header) { h.Del("X-Moox-Target-Node") },
		"missing signature": func(h http.Header) { h.Del("X-Moox-Signature") },
		"bad signature":     func(h http.Header) { h.Set("X-Moox-Signature", strings.Repeat("0", 64)) },
		"duplicate key id":  func(h http.Header) { h.Add("X-Moox-Key-Id", "other") },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			header := valid.Clone()
			mutate(header)
			if _, err := Verify(credentials, request, header, now); err == nil {
				t.Fatal("Verify() accepted malformed headers")
			}
		})
	}
}
