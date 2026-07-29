package storageauth

import (
	"os"
	"strings"

	"github.com/mooyang-code/moox/packages/commonpb"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
)

const primarySecretEnv = "MOOX_STORAGE_PRIMARY_AUTH_SECRET"

func Primary(appID string) *commonpb.AuthInfo {
	appID = strings.TrimSpace(appID)
	auth := &commonpb.AuthInfo{AppId: appID}
	if secret := strings.TrimSpace(os.Getenv(primarySecretEnv)); secret != "" && appID != "" {
		auth.AppKey = mooxsecurity.HMACSHA256Hex(secret, []byte(appID))
	}
	return auth
}
