package configcrypt

import (
	"errors"
	"strings"
	"testing"
)

func TestBackupRoundTripsWithTheAdminPasswordOnly(t *testing.T) {
	envelope, err := SealBackup([]byte(sample), "Adm1n-P@ss")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(envelope, backupMagic+"\nkdf: argon2id ") || strings.Contains(envelope, "Adm1n-P@ss") || strings.Contains(envelope, "[admin]") {
		t.Fatalf("备份信封不应含明文:\n%s", envelope)
	}
	plain, err := OpenBackup(envelope, "Adm1n-P@ss")
	if err != nil || string(plain) != sample {
		t.Fatalf("解开 = %q, %v", plain, err)
	}
	if _, err := OpenBackup(envelope, "wrong"); !errors.Is(err, ErrBackupPassword) {
		t.Fatalf("密码不对时应提示，实际 %v", err)
	}
	if _, err := SealBackup([]byte(sample), ""); err == nil {
		t.Fatal("空密码不能加密备份")
	}
}

func TestBackupRejectsTamperingAndWeakenedParameters(t *testing.T) {
	envelope, err := SealBackup([]byte(sample), "pw")
	if err != nil {
		t.Fatal(err)
	}
	weakened := strings.Replace(envelope, "t=3", "t=1", 1)
	if _, err := OpenBackup(weakened, "pw"); err == nil {
		t.Fatal("加密参数被改动时应拒绝")
	}
	lines := strings.Split(envelope, "\n")
	body := []byte(lines[2])
	body[5] ^= 1
	lines[2] = string(body)
	if _, err := OpenBackup(strings.Join(lines, "\n"), "pw"); err == nil {
		t.Fatal("密文被改动后应解不开")
	}
	for _, bad := range []string{"", "MOOX-CONFIG-BACKUP v1\n", sample} {
		if _, err := OpenBackup(bad, "pw"); err == nil {
			t.Fatalf("%q 应被拒绝", bad)
		}
	}
}
