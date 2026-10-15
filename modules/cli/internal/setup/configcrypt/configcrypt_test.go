package configcrypt

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const sample = "[admin]\nusername = \"admin\"\npassword = \"Adm1n-P@ss\"\n"

func TestEncryptRoundTripsAndHidesThePlaintext(t *testing.T) {
	store := &MemoryStore{}
	sealed, err := Encrypt([]byte(sample), store)
	if err != nil {
		t.Fatal(err)
	}
	if !IsEncrypted(sealed) || strings.Contains(string(sealed), "Adm1n-P@ss") || strings.Contains(string(sealed), "[admin]") {
		t.Fatalf("加密后的内容不应含明文:\n%s", sealed)
	}
	for _, line := range strings.Split(strings.TrimRight(string(sealed), "\n"), "\n") {
		if len(line) > lineWidth {
			t.Fatalf("每行不应超过 %d 个字符: %q", lineWidth, line)
		}
	}
	plain, err := Decrypt(sealed, store)
	if err != nil || string(plain) != sample {
		t.Fatalf("解密结果 = %q, %v", plain, err)
	}
	again, _ := Encrypt([]byte(sample), store)
	if string(again) == string(sealed) {
		t.Fatal("每次加密都应使用新的随机数")
	}
}

func TestDecryptRejectsTamperingAndForeignKeys(t *testing.T) {
	store := &MemoryStore{}
	sealed, err := Encrypt([]byte(sample), store)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(sealed), "\n")
	body := []byte(lines[2])
	body[10] ^= 1
	lines[2] = string(body)
	if _, err := Decrypt([]byte(strings.Join(lines, "\n")), store); err == nil {
		t.Fatal("密文被改动后应解密失败")
	}
	changedHeader := strings.Replace(string(sealed), "key: ", "key: 0", 1)
	if _, err := Decrypt([]byte(changedHeader), store); err == nil {
		t.Fatal("头部被改动后应解密失败")
	}
	if _, err := Decrypt(sealed, &MemoryStore{}); !errors.Is(err, ErrKeyMissing) {
		t.Fatalf("没有密钥时应提示密钥缺失，实际 %v", err)
	}
	other := &MemoryStore{}
	_, _ = other.LoadOrCreate()
	if _, err := Decrypt(sealed, other); !errors.Is(err, ErrKeyMissing) {
		t.Fatalf("换了一把密钥时应提示密钥不对，实际 %v", err)
	}
	if _, err := Decrypt([]byte(sample), store); err == nil {
		t.Fatal("明文不是加密信封")
	}
	if _, err := Decrypt([]byte(magic+"\n"), store); err == nil {
		t.Fatal("不完整的信封应报错")
	}
}

func TestFileStoreCreatesAPrivateKeyOnce(t *testing.T) {
	store := fileStore{path: filepath.Join(t.TempDir(), "moox", "config.key")}
	if _, err := store.Load(); !errors.Is(err, ErrKeyMissing) {
		t.Fatalf("还没有密钥时应返回 ErrKeyMissing，实际 %v", err)
	}
	first, err := store.LoadOrCreate()
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.LoadOrCreate()
	if err != nil || string(first) != string(second) {
		t.Fatal("第二次应读到同一把密钥")
	}
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(store.path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("密钥文件权限应为 0600: %v %v", info.Mode(), err)
	}
	if err := os.Chmod(store.path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("别人可读的密钥文件应拒绝使用")
	}
}

func TestSetDefaultForTestReplacesAndRestores(t *testing.T) {
	memory := &MemoryStore{}
	restore := SetDefaultForTest(memory)
	if Default() != memory {
		t.Fatal("应返回替换后的密钥存放位置")
	}
	restore()
}
