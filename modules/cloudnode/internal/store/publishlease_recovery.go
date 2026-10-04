package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"gorm.io/gorm"
)

// ReplaceNodeBatchItemPublishFence atomically transfers one running durable
// item's fencing envelope to a newly acquired lease. All other request fields
// remain untouched, and the exact old request is the compare-and-swap key.
func (r *CatalogRepository) ReplaceNodeBatchItemPublishFence(
	ctx context.Context,
	spaceID, jobID, itemID string,
	expectedLeaseID string,
	expectedToken int64,
	newLeaseID string,
	newToken int64,
) (*NodeBatchItem, error) {
	if spaceID == "" || jobID == "" || itemID == "" || expectedLeaseID == "" || expectedToken < 1 || newLeaseID == "" || newToken <= expectedToken {
		return nil, fmt.Errorf("publish fence replacement identity is invalid")
	}
	var replaced NodeBatchItem
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var item NodeBatchItem
		if err := tx.Where("c_space_id = ? AND c_job_id = ? AND c_item_id = ?", spaceID, jobID, itemID).First(&item).Error; err != nil {
			return err
		}
		if item.Status != NodeBatchRunning {
			return fmt.Errorf("publish fence can only be replaced for a running node batch item")
		}
		request, leaseKey, tokenKey, err := mutablePublishFenceEnvelope(item.RequestJSON)
		if err != nil {
			return err
		}
		currentLeaseID, currentToken, err := readPublishFence(request, leaseKey, tokenKey)
		if err != nil {
			return err
		}
		if currentLeaseID != expectedLeaseID || currentToken != expectedToken {
			return fmt.Errorf("node batch item publish fence changed before recovery")
		}
		leaseValue, _ := json.Marshal(newLeaseID)
		tokenValue, _ := json.Marshal(strconv.FormatInt(newToken, 10))
		request[leaseKey] = leaseValue
		request[tokenKey] = tokenValue
		encoded, err := json.Marshal(request)
		if err != nil {
			return fmt.Errorf("encode recovered publish fence: %w", err)
		}
		now := time.Now().UTC()
		result := tx.Model(&NodeBatchItem{}).
			Where("c_space_id = ? AND c_job_id = ? AND c_item_id = ? AND c_status = ? AND c_request_json = ?", spaceID, jobID, itemID, NodeBatchRunning, item.RequestJSON).
			Updates(map[string]any{
				"c_request_json":                 string(encoded),
				"c_publish_lease_recovery_owned": true,
				"c_mtime":                        now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("node batch item changed while recovering its publish fence")
		}
		item.RequestJSON = string(encoded)
		item.PublishLeaseRecoveryOwned = true
		item.ModifyTime = now
		replaced = item
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &replaced, nil
}

// ListTerminalNodeBatchItemsForPublishLeaseRelease finds recovery-owned leases
// whose item has reached a terminal state but whose release was interrupted.
func (r *CatalogRepository) ListTerminalNodeBatchItemsForPublishLeaseRelease(ctx context.Context, limit int) ([]NodeBatchItem, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("publish lease release limit must be positive")
	}
	var items []NodeBatchItem
	err := r.db.WithContext(ctx).
		Where("c_publish_lease_recovery_owned = ? AND c_status IN ?", true, []string{NodeBatchSuccess, NodeBatchFailed}).
		Order("c_id ASC").Limit(limit).Find(&items).Error
	return items, err
}

// MarkNodeBatchItemPublishLeaseReleased clears durable release work only when
// the item still carries the exact recovered lease being released.
func (r *CatalogRepository) MarkNodeBatchItemPublishLeaseReleased(
	ctx context.Context,
	spaceID, jobID, itemID, leaseID string,
	token int64,
) error {
	if spaceID == "" || jobID == "" || itemID == "" || leaseID == "" || token < 1 {
		return fmt.Errorf("publish lease release identity is invalid")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var item NodeBatchItem
		if err := tx.Where("c_space_id = ? AND c_job_id = ? AND c_item_id = ?", spaceID, jobID, itemID).First(&item).Error; err != nil {
			return err
		}
		if !item.PublishLeaseRecoveryOwned {
			return nil
		}
		if item.Status != NodeBatchSuccess && item.Status != NodeBatchFailed {
			return fmt.Errorf("recovered publish lease cannot be cleared before item terminal state")
		}
		request, leaseKey, tokenKey, err := mutablePublishFenceEnvelope(item.RequestJSON)
		if err != nil {
			return err
		}
		currentLeaseID, currentToken, err := readPublishFence(request, leaseKey, tokenKey)
		if err != nil {
			return err
		}
		if currentLeaseID != leaseID || currentToken != token {
			return fmt.Errorf("node batch item publish fence changed before lease release was recorded")
		}
		result := tx.Model(&NodeBatchItem{}).
			Where("c_space_id = ? AND c_job_id = ? AND c_item_id = ? AND c_status IN ? AND c_publish_lease_recovery_owned = ? AND c_request_json = ?", spaceID, jobID, itemID, []string{NodeBatchSuccess, NodeBatchFailed}, true, item.RequestJSON).
			Update("c_publish_lease_recovery_owned", false)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("node batch item changed before recovered lease release was recorded")
		}
		return nil
	})
}

func mutablePublishFenceEnvelope(encoded string) (map[string]json.RawMessage, string, string, error) {
	var request map[string]json.RawMessage
	if err := json.Unmarshal([]byte(encoded), &request); err != nil {
		return nil, "", "", fmt.Errorf("decode node batch request publish fence: %w", err)
	}
	leaseKey, tokenKey := "collectorPublishLeaseId", "collectorPublishFencingToken"
	if _, ok := request[leaseKey]; !ok {
		leaseKey, tokenKey = "collector_publish_lease_id", "collector_publish_fencing_token"
	}
	if _, ok := request[leaseKey]; !ok {
		return nil, "", "", fmt.Errorf("node batch request has no publish lease envelope")
	}
	if _, ok := request[tokenKey]; !ok {
		return nil, "", "", fmt.Errorf("node batch request has no fencing token envelope")
	}
	return request, leaseKey, tokenKey, nil
}

func readPublishFence(request map[string]json.RawMessage, leaseKey, tokenKey string) (string, int64, error) {
	var leaseID string
	if err := json.Unmarshal(request[leaseKey], &leaseID); err != nil {
		return "", 0, fmt.Errorf("decode node batch request lease id: %w", err)
	}
	var token int64
	if err := json.Unmarshal(request[tokenKey], &token); err != nil {
		var tokenText string
		if stringErr := json.Unmarshal(request[tokenKey], &tokenText); stringErr != nil {
			return "", 0, fmt.Errorf("decode node batch request fencing token: %w", err)
		}
		var parseErr error
		token, parseErr = strconv.ParseInt(tokenText, 10, 64)
		if parseErr != nil {
			return "", 0, fmt.Errorf("decode node batch request fencing token: %w", parseErr)
		}
	}
	return leaseID, token, nil
}
