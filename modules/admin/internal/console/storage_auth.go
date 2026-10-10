package console

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
)

const (
	storageViewServicePath = "trpc.moox.storage.DataView"
	storageGatewayAppID    = "admin-gateway"
)

// signStorageBody 用 Admin 持有的 Storage 角色密钥重写请求里的 auth_info。
// 浏览器无法也不应持有 Storage 角色密钥，到达这里的请求已通过用户会话与空间授权。
func signStorageBody(servicePath string, body []byte) ([]byte, error) {
	envName := "MOOX_STORAGE_PRIMARY_AUTH_SECRET"
	if servicePath == storageViewServicePath {
		envName = "MOOX_STORAGE_VIEW_AUTH_SECRET"
	}
	secret := strings.TrimSpace(os.Getenv(envName))
	if secret == "" {
		return nil, errors.New("Storage 内部鉴权密钥未配置: " + envName)
	}
	payload := make(map[string]json.RawMessage)
	if len(strings.TrimSpace(string(body))) != 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, err
		}
	}
	auth := make(map[string]json.RawMessage)
	if raw := payload["auth_info"]; len(raw) != 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &auth); err != nil {
			return nil, err
		}
	}
	auth["app_id"], _ = json.Marshal(storageGatewayAppID)
	auth["app_key"], _ = json.Marshal(storageServiceAuthKey(secret, storageGatewayAppID))
	payload["auth_info"], _ = json.Marshal(auth)
	return json.Marshal(payload)
}

func storageServiceAuthKey(secret, appID string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(appID))
	return hex.EncodeToString(mac.Sum(nil))
}
