package adminclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

type RevealedSecret struct {
	SecretID    string `json:"secret_id"`
	Category    string `json:"category"`
	Provider    string `json:"provider"`
	Status      string `json:"status"`
	KeyID       string `json:"key_id"`
	SecretValue string `json:"secret_value"`
}

func (c *Client) RevealSecret(ctx context.Context, secretID string) (*RevealedSecret, error) {
	if c.ServiceAuth == nil {
		return nil, fmt.Errorf("service authentication is required to reveal secrets")
	}
	raw, err := c.postJSON(ctx, http.MethodPost, "/api/admin/secret/RevealSecret", map[string]any{"secret_id": secretID})
	if err != nil {
		return nil, err
	}
	var response struct {
		RetInfo *retInfo        `json:"ret_info"`
		Secret  *RevealedSecret `json:"secret"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	if response.RetInfo == nil || !isRetInfoSuccess(response.RetInfo.Code) || response.Secret == nil {
		return nil, fmt.Errorf("RevealSecret rejected")
	}
	if response.Secret.Status != "active" || response.Secret.Category != "cloud" || response.Secret.Provider != "tencent" {
		return nil, fmt.Errorf("secret must be active category=cloud provider=tencent")
	}
	return response.Secret, nil
}
