package crypto

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	ciphertext, err := Encrypt("hello-moox", "human-readable-secret")
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := Decrypt(ciphertext, "human-readable-secret")
	if err != nil {
		t.Fatal(err)
	}
	if plaintext != "hello-moox" {
		t.Fatalf("plaintext=%q", plaintext)
	}
}

func TestEncryptUsesRandomNonce(t *testing.T) {
	first, err := Encrypt("same", "same-secret")
	if err != nil {
		t.Fatal(err)
	}
	second, err := Encrypt("same", "same-secret")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("encryption reused a nonce")
	}
}

func TestDecryptRejectsInvalidInputs(t *testing.T) {
	tests := []struct {
		name, ciphertext, secret string
	}{
		{name: "empty ciphertext", ciphertext: "", secret: "secret"},
		{name: "invalid base64", ciphertext: "%%%", secret: "secret"},
		{name: "short payload", ciphertext: base64.StdEncoding.EncodeToString([]byte("short")), secret: "secret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Decrypt(tt.ciphertext, tt.secret); err == nil {
				t.Fatal("Decrypt returned nil error")
			}
		})
	}
	if _, err := Encrypt("", "secret"); err == nil {
		t.Fatal("Encrypt accepted empty plaintext")
	}
	if _, err := Encrypt("plaintext", ""); err == nil {
		t.Fatal("Encrypt accepted empty secret")
	}
}

func TestWrongSecretCannotDecrypt(t *testing.T) {
	ciphertext, err := Encrypt("secret", "key-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decrypt(ciphertext, "key-b"); err == nil {
		t.Fatal("Decrypt accepted the wrong secret")
	}
}

func TestDecryptWebCryptoVector(t *testing.T) {
	const webCryptoPayload = "AAAAAAAAAAAAAAAAYw0AaFU9nY6+Pswe4DHAqf4n2z8xQ0UH/Q=="
	plaintext, err := Decrypt(webCryptoPayload, "test-salt1700000000")
	if err != nil {
		t.Fatal(err)
	}
	if plaintext != "secret123" {
		t.Fatalf("plaintext=%q", plaintext)
	}
}

func TestPasswordHashAndVerify(t *testing.T) {
	hash, err := HashPassword("password")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword("password", hash) {
		t.Fatal("VerifyPassword rejected the correct password")
	}
	if VerifyPassword("wrong", hash) {
		t.Fatal("VerifyPassword accepted the wrong password")
	}
	if strings.HasPrefix(hash, "sha256:") {
		t.Fatal("password hash still uses the old format")
	}
}

func TestTokenSignAndParse(t *testing.T) {
	token, err := SignToken(map[string]any{"user_id": "u1", "token_type": "access"}, "jwt-secret", "moox-admin", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := ParseToken(token, "jwt-secret")
	if err != nil {
		t.Fatal(err)
	}
	if claims["user_id"] != "u1" || claims["token_type"] != "access" {
		t.Fatalf("claims=%v", claims)
	}
	if _, err := ParseToken(token, "wrong-secret"); err == nil {
		t.Fatal("ParseToken accepted the wrong secret")
	}
}

func TestParseTokenRejectsNonHS256(t *testing.T) {
	token := "eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0.eyJzdWIiOiIxIn0."
	if _, err := ParseToken(token, "secret"); err == nil {
		t.Fatal("ParseToken accepted a non-HS256 token")
	}
}

func TestNewSaltAndMaskSecret(t *testing.T) {
	first, err := NewSalt()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewSalt()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 32 || len(second) != 32 || first == second {
		t.Fatalf("salts=%q,%q", first, second)
	}
	if got := MaskSecret("abcdefghijklwxyz", 4, 4); got != "abcd****wxyz" {
		t.Fatalf("MaskSecret=%q", got)
	}
	if got := MaskSecret("short", 4, 4); got != "****" {
		t.Fatalf("short MaskSecret=%q", got)
	}
}
