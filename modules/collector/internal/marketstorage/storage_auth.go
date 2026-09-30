package marketstorage

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
)

func parseStoragePrimaryAppKeys(raw string) (map[string]string, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("storage app keys must be a JSON object")
	}
	keys := make(map[string]string)
	for decoder.More() {
		token, err := decoder.Token()
		appID, ok := token.(string)
		if err != nil || !ok || strings.TrimSpace(appID) == "" {
			return nil, errors.New("invalid storage app ID")
		}
		if _, duplicate := keys[appID]; duplicate {
			return nil, errors.New("duplicate storage app ID")
		}
		var appKey string
		if err := decoder.Decode(&appKey); err != nil || len(appKey) != 64 {
			return nil, errors.New("storage app key must be 64 hex characters")
		}
		if _, err := hex.DecodeString(appKey); err != nil {
			return nil, errors.New("storage app key must be 64 hex characters")
		}
		keys[appID] = appKey
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, errors.New("invalid storage app-key object")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing storage app-key JSON")
	}
	return keys, nil
}

func storageAuthInfo(binding StorageBinding) (*storagepb.AuthInfo, error) {
	if strings.TrimSpace(binding.AuthInfo.AppID) == "" {
		return nil, errors.New("storage binding app ID is required")
	}
	appKey := binding.AuthInfo.AppKey
	// A present managed object is authoritative, including an explicitly empty value.
	if raw, exists := os.LookupEnv("MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON"); exists {
		keys, err := parseStoragePrimaryAppKeys(raw)
		if err != nil {
			return nil, err
		}
		var found bool
		appKey, found = keys[binding.AuthInfo.AppID]
		if !found {
			return nil, errors.New("storage app keys do not contain the binding app ID")
		}
	} else if secret := strings.TrimSpace(os.Getenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET")); secret != "" {
		appKey = mooxsecurity.HMACSHA256Hex(secret, []byte(binding.AuthInfo.AppID))
	}
	if strings.TrimSpace(appKey) == "" {
		return nil, errors.New("storage binding app key is required")
	}
	return &storagepb.AuthInfo{
		AppId: binding.AuthInfo.AppID, AppKey: appKey,
		Operator: binding.AuthInfo.Operator, RequestId: binding.AuthInfo.RequestID,
	}, nil
}
