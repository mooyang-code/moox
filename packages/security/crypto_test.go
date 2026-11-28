package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"regexp"
	"strings"
	"testing"

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

func TestDecryptRejectsNonCanonicalKeyDerivation(t *testing.T) {
	secret := "0123456789abcdef"
	block, err := aes.NewCipher([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		t.Fatal(err)
	}
	payload := gcm.Seal(nonce, nonce, []byte("noncanonical-secret"), nil)
	if _, err := Decrypt(base64.StdEncoding.EncodeToString(payload), secret); err == nil {
		t.Fatal("Decrypt accepted a noncanonical key derivation")
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

func TestSHA256Hex(t *testing.T) {
	if got := SHA256Hex([]byte("abc")); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("SHA256Hex() = %q", got)
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
