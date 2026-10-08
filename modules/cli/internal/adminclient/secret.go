package adminclient

import (
	"context"
	"encoding/json"
	"fmt"
)

type SecretMaterial struct {
	SecretID    string `json:"secret_id"`
	Category    string `json:"category"`
	Provider    string `json:"provider"`
	Status      string `json:"status"`
	KeyID       string `json:"key_id"`
	SecretValue string `json:"secret_value"`
}

func (c *Client) GetSecretValue(ctx context.Context, secretID string) (*SecretMaterial, error) {
	raw, err := c.call(ctx, ServiceSecretMgr, "GetSecretValue", map[string]any{"secret_id": secretID})
	if err != nil {
		return nil, err
	}
	var response struct {
		RetInfo *retInfo        `json:"ret_info"`
		Secret  *SecretMaterial `json:"secret"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	if response.RetInfo == nil || !isRetInfoSuccess(response.RetInfo.Code) || response.Secret == nil {
		return nil, fmt.Errorf("GetSecretValue rejected")
	}
	if response.Secret.Status != "active" || response.Secret.Category != "cloud" || response.Secret.Provider != "tencent" {
		return nil, fmt.Errorf("secret must be active category=cloud provider=tencent")
	}
	return response.Secret, nil
}
