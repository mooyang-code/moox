package impl

import (
	"context"
	"strconv"

	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"trpc.group/trpc-go/trpc-go/log"
)

func encryptedPasswordSecret(salt string, timestamp int64) string {
	return salt + strconv.FormatInt(timestamp, 10)
}

func decryptPassword(encryptedPassword, salt string, timestamp int64) (string, error) {
	return mooxsecurity.Decrypt(encryptedPassword, encryptedPasswordSecret(salt, timestamp))
}

func validateEncryptedPassword(ctx context.Context, storedHash, salt string, timestamp int64, encryptedPassword string) bool {
	password, err := decryptPassword(encryptedPassword, salt, timestamp)
	if err != nil {
		log.ErrorContextf(ctx, "[Auth] 密码解密失败: %v", err)
		return false
	}
	return mooxsecurity.VerifyPassword(password, storedHash)
}
