package adminclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type CollectorPublishLease struct {
	SpaceID      string
	LeaseID      string
	HolderID     string
	FencingToken int64
	ExpiresAt    time.Time
}

type CollectorPublishFence struct {
	LeaseID      string
	FencingToken int64
}

func (c *Client) SetCollectorPublishLease(lease *CollectorPublishLease) {
	if c == nil {
		return
	}
	c.publishLeaseMu.Lock()
	defer c.publishLeaseMu.Unlock()
	if lease == nil {
		c.publishLease = nil
		return
	}
	copy := *lease
	c.publishLease = &copy
}

func (c *Client) CollectorPublishFence() *CollectorPublishFence {
	if c == nil {
		return nil
	}
	c.publishLeaseMu.RLock()
	defer c.publishLeaseMu.RUnlock()
	if c.publishLease == nil {
		return nil
	}
	return &CollectorPublishFence{LeaseID: c.publishLease.LeaseID, FencingToken: c.publishLease.FencingToken}
}

func (c *Client) withPublishFenceToCreateItems(items []NodeCreateItem) []NodeCreateItem {
	fence := c.CollectorPublishFence()
	if fence == nil {
		return items
	}
	items = append([]NodeCreateItem(nil), items...)
	for index := range items {
		items[index].CollectorPublishLeaseID = fence.LeaseID
		items[index].CollectorPublishFencingToken = fence.FencingToken
	}
	return items
}

func (c *Client) withPublishFenceToDeployItems(items []NodeDeployItem) []NodeDeployItem {
	fence := c.CollectorPublishFence()
	if fence == nil {
		return items
	}
	items = append([]NodeDeployItem(nil), items...)
	for index := range items {
		items[index].CollectorPublishLeaseID = fence.LeaseID
		items[index].CollectorPublishFencingToken = fence.FencingToken
	}
	return items
}

func (c *Client) AcquireCollectorPublishLease(ctx context.Context, spaceID, holderID string) (*CollectorPublishLease, error) {
	spaceID, holderID = strings.TrimSpace(spaceID), strings.TrimSpace(holderID)
	if spaceID == "" || holderID == "" {
		return nil, fmt.Errorf("space_id and holder_id are required")
	}
	raw, err := c.gatewayJSON(ctx, "trpc.moox.admin.CollectorPublishLease", "AcquireCollectorPublishLease", map[string]any{
		"space_id": spaceID, "holder_id": holderID,
	})
	if err != nil {
		return nil, fmt.Errorf("AcquireCollectorPublishLease: %w", err)
	}
	lease, err := decodeCollectorPublishLease(raw, "AcquireCollectorPublishLease")
	if err == nil && lease.SpaceID != spaceID {
		return nil, fmt.Errorf("AcquireCollectorPublishLease returned a different space")
	}
	return lease, err
}

func (c *Client) RenewCollectorPublishLease(ctx context.Context, lease *CollectorPublishLease) (*CollectorPublishLease, error) {
	if lease == nil || lease.SpaceID == "" || lease.LeaseID == "" || lease.FencingToken < 1 {
		return nil, fmt.Errorf("collector publish lease identity is incomplete")
	}
	raw, err := c.gatewayJSON(ctx, "trpc.moox.admin.CollectorPublishLease", "RenewCollectorPublishLease", map[string]any{
		"space_id": lease.SpaceID, "lease_id": lease.LeaseID, "fencing_token": strconv.FormatInt(lease.FencingToken, 10),
	})
	if err != nil {
		return nil, fmt.Errorf("RenewCollectorPublishLease: %w", err)
	}
	renewed, err := decodeCollectorPublishLease(raw, "RenewCollectorPublishLease")
	if err != nil {
		return nil, err
	}
	if renewed.SpaceID != lease.SpaceID || renewed.LeaseID != lease.LeaseID || renewed.FencingToken != lease.FencingToken {
		return nil, fmt.Errorf("RenewCollectorPublishLease returned a different lease identity")
	}
	renewed.HolderID = lease.HolderID
	return renewed, nil
}

func (c *Client) ReleaseCollectorPublishLease(ctx context.Context, lease *CollectorPublishLease) error {
	if lease == nil || lease.SpaceID == "" || lease.LeaseID == "" || lease.FencingToken < 1 {
		return fmt.Errorf("collector publish lease identity is incomplete")
	}
	raw, err := c.gatewayJSON(ctx, "trpc.moox.admin.CollectorPublishLease", "ReleaseCollectorPublishLease", map[string]any{
		"space_id": lease.SpaceID, "lease_id": lease.LeaseID, "fencing_token": strconv.FormatInt(lease.FencingToken, 10),
	})
	if err != nil {
		return fmt.Errorf("ReleaseCollectorPublishLease: %w", err)
	}
	var response struct {
		RetInfo  *retInfo `json:"ret_info"`
		Released bool     `json:"released"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return fmt.Errorf("decode ReleaseCollectorPublishLease: %w", err)
	}
	if response.RetInfo == nil || !isRetInfoSuccess(response.RetInfo.Code) {
		return fmt.Errorf("ReleaseCollectorPublishLease rejected: %s", safeLeaseMessage(response.RetInfo))
	}
	if !response.Released {
		return fmt.Errorf("ReleaseCollectorPublishLease: lease is no longer current")
	}
	c.SetCollectorPublishLease(nil)
	return nil
}

func decodeCollectorPublishLease(raw []byte, method string) (*CollectorPublishLease, error) {
	var response struct {
		RetInfo      *retInfo        `json:"ret_info"`
		SpaceID      string          `json:"space_id"`
		LeaseID      string          `json:"lease_id"`
		FencingToken json.RawMessage `json:"fencing_token"`
		ExpiresAt    string          `json:"expires_at"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("decode %s: %w", method, err)
	}
	if response.RetInfo == nil || !isRetInfoSuccess(response.RetInfo.Code) {
		return nil, fmt.Errorf("%s rejected: %s", method, safeLeaseMessage(response.RetInfo))
	}
	tokenText := strings.Trim(string(response.FencingToken), `"`)
	token, err := strconv.ParseInt(tokenText, 10, 64)
	if err != nil || token < 1 {
		return nil, fmt.Errorf("%s returned an invalid fencing token", method)
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, response.ExpiresAt)
	if err != nil || response.SpaceID == "" || response.LeaseID == "" {
		return nil, fmt.Errorf("%s returned an incomplete lease", method)
	}
	return &CollectorPublishLease{SpaceID: response.SpaceID, LeaseID: response.LeaseID, FencingToken: token, ExpiresAt: expiresAt}, nil
}

func safeLeaseMessage(info *retInfo) string {
	if info == nil {
		return "missing ret_info"
	}
	return info.Msg
}
