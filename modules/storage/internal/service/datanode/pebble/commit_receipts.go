package pebble

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"

	cpebble "github.com/cockroachdb/pebble"
)

const (
	writeReceiptPrefix   = "__write_receipt/"
	writeReceiptBodyPref = "__write_receipt_body/"
)

type commitPosition struct {
	NodeID   string
	StoreID  string
	Sequence uint64
}

type commitReceipt struct {
	CommitID  string
	SpaceID   string
	DatasetID string
	Position  commitPosition
	WriteKind string
}

type persistedReceipt struct {
	CommitID  string `json:"commit_id"`
	SpaceID   string `json:"space_id,omitempty"`
	DatasetID string `json:"dataset_id,omitempty"`
	NodeID    string `json:"node_id"`
	StoreID   string `json:"store_id"`
	Sequence  uint64 `json:"sequence"`
	WriteKind string `json:"write_kind"`
}

func (s *Store) loadReceiptLocked(commitID string) (*commitReceipt, []byte, error) {
	data, closer, err := s.db.Get([]byte(writeReceiptPrefix + commitID))
	if errors.Is(err, cpebble.ErrNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	copied := append([]byte(nil), data...)
	_ = closer.Close()
	stored := persistedReceipt{}
	if err := json.Unmarshal(copied, &stored); err != nil {
		return nil, nil, err
	}
	body, closer, err := s.db.Get([]byte(writeReceiptBodyPref + commitID))
	if err != nil {
		return nil, nil, err
	}
	fingerprint := append([]byte(nil), body...)
	_ = closer.Close()
	return &commitReceipt{
		CommitID:  stored.CommitID,
		SpaceID:   stored.SpaceID,
		DatasetID: stored.DatasetID,
		Position:  commitPosition{NodeID: stored.NodeID, StoreID: stored.StoreID, Sequence: stored.Sequence},
		WriteKind: stored.WriteKind,
	}, fingerprint, nil
}

// receiptDigest reduces a canonical write payload to the fixed-size value kept
// beside the receipt, so retries can be compared without retaining whole batches.
func receiptDigest(fingerprint []byte) []byte {
	sum := sha256.Sum256(fingerprint)
	return sum[:]
}

func persistReceipt(batch *cpebble.Batch, opts *cpebble.WriteOptions, receipt *commitReceipt, fingerprint []byte) error {
	payload, err := json.Marshal(persistedReceipt{
		CommitID: receipt.CommitID, SpaceID: receipt.SpaceID, DatasetID: receipt.DatasetID, NodeID: receipt.Position.NodeID, StoreID: receipt.Position.StoreID,
		Sequence: receipt.Position.Sequence, WriteKind: receipt.WriteKind,
	})
	if err != nil {
		return err
	}
	if err := batch.Set([]byte(writeReceiptPrefix+receipt.CommitID), payload, opts); err != nil {
		return err
	}
	return batch.Set([]byte(writeReceiptBodyPref+receipt.CommitID), receiptDigest(fingerprint), opts)
}

func validateCommitID(commitID string) error {
	if strings.TrimSpace(commitID) == "" || strings.TrimSpace(commitID) != commitID || strings.ContainsAny(commitID, "/\x00") {
		return invalid("commit_id is invalid")
	}
	return nil
}
