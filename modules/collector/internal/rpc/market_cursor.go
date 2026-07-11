package rpc

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

type marketCursor struct {
	QueryHash           string            `json:"query_hash"`
	QueryAsOf           string            `json:"query_as_of"`
	Offset              int               `json:"offset"`
	BoundaryKey         string            `json:"boundary_key,omitempty"`
	BoundaryDigest      string            `json:"boundary_digest,omitempty"`
	PrefixDigest        string            `json:"prefix_digest,omitempty"`
	DatasetOffsets      map[string]int    `json:"dataset_offsets,omitempty"`
	BoundaryDataset     string            `json:"boundary_dataset,omitempty"`
	BoundaryInstrument  string            `json:"boundary_instrument,omitempty"`
	BoundarySubject     string            `json:"boundary_subject,omitempty"`
	BoundaryFrequency   string            `json:"boundary_frequency,omitempty"`
	BoundaryDataTime    string            `json:"boundary_data_time,omitempty"`
	BoundaryDimensions  map[string]string `json:"boundary_dimensions,omitempty"`
	StreamPrefixDigests map[string]string `json:"stream_prefix_digests,omitempty"`
}

func encodeMarketCursor(value marketCursor) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeMarketCursor(raw string) (marketCursor, error) {
	value, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return marketCursor{}, fmt.Errorf("invalid market cursor: %w", err)
	}
	var cursor marketCursor
	if err := json.Unmarshal(value, &cursor); err != nil {
		return marketCursor{}, fmt.Errorf("invalid market cursor: %w", err)
	}
	if cursor.QueryHash == "" || cursor.QueryAsOf == "" || cursor.Offset < 0 {
		return marketCursor{}, fmt.Errorf("invalid market cursor fields")
	}
	return cursor, nil
}

func marketQueryHash(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
