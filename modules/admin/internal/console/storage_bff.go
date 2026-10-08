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

// storageConsoleAppID 是控制台在 Storage 应用层鉴权（auth_info）中的身份。
const storageConsoleAppID = "console"

// storageBFFBody 为浏览器的 Storage 请求注入 auth_info：Storage 在主机网关之外还按应用身份做一层鉴权，
// 例如手动重建视图只接受控制台和 CLI。密钥按目标服务所属的 Storage 角色选择。
func storageBFFBody(servicePath string, body []byte) ([]byte, error) {
	var env string
	switch servicePath {
	case "trpc.moox.storage.Metadata", "trpc.moox.storage.PrimaryStore":
		env = "MOOX_STORAGE_PRIMARY_AUTH_SECRET"
	case "trpc.moox.storage.DataView":
		env = "MOOX_STORAGE_VIEW_AUTH_SECRET"
	default:
		return nil, errors.New("不支持的 Storage 服务")
	}
	secret := strings.TrimSpace(os.Getenv(env))
	if secret == "" {
		return nil, errors.New("控制台没有配置 Storage 应用鉴权密钥")
	}
	payload := make(map[string]json.RawMessage)
	if len(body) != 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, err
		}
	}
	authPayload := make(map[string]json.RawMessage)
	if raw := payload["auth_info"]; len(raw) != 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &authPayload); err != nil {
			return nil, err
		}
	}
	authPayload["app_id"], _ = json.Marshal(storageConsoleAppID)
	authPayload["app_key"], _ = json.Marshal(storageServiceAuthKey(secret, storageConsoleAppID))
	payload["auth_info"], _ = json.Marshal(authPayload)
	return json.Marshal(payload)
}

func storageServiceAuthKey(secret, appID string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(appID))
	return hex.EncodeToString(mac.Sum(nil))
}
