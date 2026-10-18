package adminpb

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"google.golang.org/protobuf/proto"
)

// SnapshotHash covers the entire snapshot, including actual verifier key
// material, while excluding its hash field. The control plane emits ordered
// routes and keys; deterministic protobuf encoding also orders directory maps.
// Receivers use the same function before accepting a snapshot. It never mutates
// the supplied message and does not replace snapshot/schema validation.
func SnapshotHash(snapshot *HostGatewaySnapshot) (string, error) {
	if snapshot == nil {
		return "", errors.New("gateway snapshot is nil")
	}
	owned := proto.Clone(snapshot).(*HostGatewaySnapshot)
	owned.Hash = ""
	raw, err := (proto.MarshalOptions{Deterministic: true}).Marshal(owned)
	if err != nil {
		return "", errors.New("encode gateway snapshot hash")
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:]), nil
}
