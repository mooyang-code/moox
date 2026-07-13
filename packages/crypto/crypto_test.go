package crypto

import (
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
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

func TestHashPasswordRejectsMoreThan72Bytes(t *testing.T) {
	_, err := HashPassword(strings.Repeat("a", 73))
	if !errors.Is(err, bcrypt.ErrPasswordTooLong) {
		t.Fatalf("HashPassword error = %v, want bcrypt.ErrPasswordTooLong", err)
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

func TestSHA256Hex(t *testing.T) {
	if got := SHA256Hex([]byte("abc")); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("SHA256Hex() = %q", got)
	}
}

func TestHMACSHA256Hex(t *testing.T) {
	if got := HMACSHA256Hex("key", []byte("The quick brown fox jumps over the lazy dog")); got != "f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8" {
		t.Fatalf("HMACSHA256Hex() = %q", got)
	}
}

func TestRandomHexShapeAndUniqueness(t *testing.T) {
	first, err := RandomHex(32)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RandomHex(32)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(first) {
		t.Fatalf("RandomHex() = %q", first)
	}
	if first == second {
		t.Fatal("RandomHex() returned a duplicate")
	}
	if _, err := RandomHex(0); err == nil {
		t.Fatal("RandomHex() accepted a non-positive size")
	}
}
