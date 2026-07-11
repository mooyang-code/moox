package pipeline

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

type Cursor struct {
	Version          int    `json:"version"`
	PlanID           string `json:"plan_id"`
	TaskIDsHash      string `json:"task_ids_hash"`
	MarketID         string `json:"market_id"`
	ProviderID       string `json:"provider_id"`
	SourceDatasetID  string `json:"source_dataset_id"`
	UnifiedDatasetID string `json:"unified_dataset_id"`
	Feed             string `json:"feed"`
	Phase            string `json:"phase"`
	LastCommittedKey string `json:"last_committed_key,omitempty"`
	Integrity        string `json:"integrity"`
}
type CursorScope struct {
	PlanID           string
	TaskIDsHash      string
	MarketID         string
	ProviderID       string
	SourceDatasetID  string
	UnifiedDatasetID string
	Feed             string
	Phase            string
}

func EncodeCursor(cursor Cursor) (string, error) {
	cursor.Integrity = ""
	raw, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	cursor.Integrity = base64.RawURLEncoding.EncodeToString(digest[:])
	raw, err = json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
func DecodeCursor(encoded string, scope CursorScope) (Cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return Cursor{}, fmt.Errorf("decode cursor: %w", err)
	}
	var cursor Cursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return Cursor{}, err
	}
	integrity := cursor.Integrity
	cursor.Integrity = ""
	canonical, err := json.Marshal(cursor)
	if err != nil {
		return Cursor{}, err
	}
	digest := sha256.Sum256(canonical)
	if integrity != base64.RawURLEncoding.EncodeToString(digest[:]) {
		return Cursor{}, fmt.Errorf("cursor integrity check failed")
	}
	if scope.PlanID != "" && cursor.PlanID != scope.PlanID || scope.TaskIDsHash != "" && cursor.TaskIDsHash != scope.TaskIDsHash || scope.MarketID != "" && cursor.MarketID != scope.MarketID || scope.ProviderID != "" && cursor.ProviderID != scope.ProviderID || scope.SourceDatasetID != "" && cursor.SourceDatasetID != scope.SourceDatasetID || scope.UnifiedDatasetID != "" && cursor.UnifiedDatasetID != scope.UnifiedDatasetID || scope.Feed != "" && cursor.Feed != scope.Feed || scope.Phase != "" && cursor.Phase != scope.Phase {
		return Cursor{}, fmt.Errorf("cursor scope does not match job item")
	}
	cursor.Integrity = integrity
	return cursor, nil
}
