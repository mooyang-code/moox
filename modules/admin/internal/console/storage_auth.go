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

// errInvalidStorageBody 表示请求体不是合法的 JSON 对象，应作为 400 返回。
var errInvalidStorageBody = errors.New("request body must be a JSON object")

const (
	storageViewServicePath = "trpc.moox.storage.DataView"
	storageGatewayAppID    = "admin-gateway"
)

// signStorageBody 用 Admin 持有的 Storage 角色密钥重写请求里的 auth_info。
// 浏览器无法也不应持有 Storage 角色密钥，到达这里的请求已通过用户会话与空间授权。
func signStorageBody(servicePath string, body []byte, injectSpace string) ([]byte, error) {
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
		if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
			return nil, errInvalidStorageBody
		}
	}
	// ProtoJSON 同时接受 snake_case 与 camelCase；先合并再删除别名，保证只输出一份。
	auth := make(map[string]json.RawMessage)
	for _, name := range []string{"auth_info", "authInfo"} {
		raw := payload[name]
		delete(payload, name)
		if len(raw) == 0 || string(raw) == "null" {
			continue
		}
		var one map[string]json.RawMessage
		if err := json.Unmarshal(raw, &one); err != nil || one == nil {
			return nil, errInvalidStorageBody
		}
		for key, value := range one {
			auth[key] = value
		}
	}
	delete(auth, "appId")
	delete(auth, "appKey")
	auth["app_id"], _ = json.Marshal(storageGatewayAppID)
	auth["app_key"], _ = json.Marshal(storageServiceAuthKey(secret, storageGatewayAppID))
	if injectSpace != "" {
		delete(payload, "spaceId")
		payload["space_id"], _ = json.Marshal(injectSpace)
	}
	payload["auth_info"], _ = json.Marshal(auth)
	return json.Marshal(payload)
}

func storageServiceAuthKey(secret, appID string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(appID))
	return hex.EncodeToString(mac.Sum(nil))
}
