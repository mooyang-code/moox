package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
)

// storageBFFMethods is the browser-facing Storage contract. The target
// service IDs are existing HTTP deployments; the BFF does not create a new
// Storage backend or expose the internal service names to the browser.
var storageBFFMethods = map[string]string{
	"CreateDataSource":       "storage-primary",
	"UpdateDataSource":       "storage-primary",
	"GetDataSource":          "storage-primary",
	"ListDataSources":        "storage-primary",
	"UpsertSubject":          "storage-primary",
	"UpsertSubjectSymbol":    "storage-primary",
	"GetSubject":             "storage-primary",
	"ListSubjects":           "storage-primary",
	"ListSubjectSymbols":     "storage-primary",
	"RegisterDataSubject":    "storage-primary",
	"CreateDataset":          "storage-primary",
	"UpdateDataset":          "storage-primary",
	"GetDataset":             "storage-primary",
	"ListDatasets":           "storage-primary",
	"BindDatasetSubject":     "storage-primary",
	"ListDatasetSubjects":    "storage-primary",
	"CreateField":            "storage-primary",
	"CreateFieldGroup":       "storage-primary",
	"UpdateFieldGroup":       "storage-primary",
	"GetFieldGroup":          "storage-primary",
	"ListFieldGroups":        "storage-primary",
	"UpdateField":            "storage-primary",
	"GetField":               "storage-primary",
	"ListFields":             "storage-primary",
	"BatchUpdateFields":      "storage-primary",
	"DeleteFieldGroup":       "storage-primary",
	"CreateFactor":           "storage-primary",
	"UpdateFactor":           "storage-primary",
	"GetFactor":              "storage-primary",
	"ListFactors":            "storage-primary",
	"UpsertDatasetColumn":    "storage-primary",
	"ListDatasetColumns":     "storage-primary",
	"CreateView":             "storage-primary",
	"UpdateView":             "storage-primary",
	"RequestViewRebuild":     "storage-primary",
	"GetView":                "storage-primary",
	"ListViews":              "storage-primary",
	"UpsertViewColumn":       "storage-primary",
	"ListViewColumns":        "storage-primary",
	"ListViewRebuildLogs":    "storage-primary",
	"GetDataNode":            "storage-primary",
	"ListDataNodes":          "storage-primary",
	"UpdateDataNode":         "storage-primary",
	"DeleteDataNode":         "storage-primary",
	"CheckDatasetActivation": "storage-primary",
	"ActivateDataset":        "storage-primary",
	"RebindDatasetDataNode":  "storage-primary",
	"RegisterArchiveFile":    "storage-primary",
	"ListArchiveFiles":       "storage-primary",
	"UpsertFields":           "storage-primary",
	"ReadFields":             "storage-primary",
	"ReadTimeSeriesRows":     "storage-primary",
	"ReadRecordRows":         "storage-primary",
	"QueryTimeSeriesRows":    "storage-view",
	"SearchRecordRows":       "storage-view",
}

func storageBFFServiceID(method string) (string, bool) {
	serviceID, ok := storageBFFMethods[method]
	return serviceID, ok
}

func storageBFFServicePath(serviceID, method string) string {
	if serviceID == "storage-view" {
		return "trpc.moox.storage.DataView"
	}
	for _, candidate := range []string{"UpsertFields", "ReadFields", "ReadTimeSeriesRows", "ReadRecordRows"} {
		if method == candidate {
			return "trpc.moox.storage.PrimaryStore"
		}
	}
	return "trpc.moox.storage.Metadata"
}

func storageBFFBody(serviceID string, body []byte) ([]byte, error) {
	var secret string
	switch serviceID {
	case "storage-primary":
		secret = strings.TrimSpace(os.Getenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET"))
	case "storage-view":
		secret = strings.TrimSpace(os.Getenv("MOOX_STORAGE_VIEW_AUTH_SECRET"))
	default:
		return nil, errors.New("unsupported Storage BFF service")
	}
	if secret == "" {
		return nil, errors.New("Storage BFF internal auth is not configured")
	}
	payload := make(map[string]json.RawMessage)
	if len(body) != 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, err
		}
	}
	const appID = "admin-gateway"
	authPayload := make(map[string]json.RawMessage)
	if raw := payload["auth_info"]; len(raw) != 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &authPayload); err != nil {
			return nil, err
		}
	}
	authPayload["app_id"], _ = json.Marshal(appID)
	authPayload["app_key"], _ = json.Marshal(storageServiceAuthKey(secret, appID))
	payload["auth_info"], _ = json.Marshal(authPayload)
	return json.Marshal(payload)
}

func storageServiceAuthKey(secret, appID string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(appID))
	return hex.EncodeToString(mac.Sum(nil))
}
