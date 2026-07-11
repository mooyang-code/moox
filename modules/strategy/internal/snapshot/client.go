package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
)

type Input struct {
	Data             []map[string]any
	Revision, Cutoff string
}

// Snapshot is an immutable point-in-time input. Data returns a detached copy
// so a strategy cannot mutate data reused by another binding.
type Snapshot struct {
	data     []map[string]any
	revision string
	cutoff   string
	hash     string
}

func Capture(in Input) (Snapshot, error) {
	if in.Revision == "" {
		return Snapshot{}, errors.New("snapshot revision is required")
	}
	data, err := cloneRows(in.Data)
	if err != nil {
		return Snapshot{}, err
	}
	b, err := json.Marshal(Input{Data: data, Revision: in.Revision, Cutoff: in.Cutoff})
	if err != nil {
		return Snapshot{}, err
	}
	h := sha256.Sum256(b)
	return Snapshot{data: data, revision: in.Revision, cutoff: in.Cutoff, hash: hex.EncodeToString(h[:])}, nil
}

func (s Snapshot) Data() []map[string]any {
	data, _ := cloneRows(s.data)
	return data
}
func (s Snapshot) Revision() string { return s.revision }
func (s Snapshot) Cutoff() string   { return s.cutoff }
func (s Snapshot) Hash() string     { return s.hash }

func Normalize(in Input) ([]map[string]any, string, error) {
	s, err := Capture(in)
	if err != nil {
		return nil, "", err
	}
	return s.Data(), s.Hash(), nil
}

func ValidateOutput(o domain.Output) error {
	if o.Action == "" || o.NextState == nil {
		return &SnapshotError{"invalid strategy snapshot output"}
	}
	return nil
}

type SnapshotError struct{ Message string }

func (e *SnapshotError) Error() string { return e.Message }

func cloneRows(rows []map[string]any) ([]map[string]any, error) {
	if rows == nil {
		return nil, nil
	}
	b, err := json.Marshal(rows)
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	// Touch keys in a deterministic order. encoding/json already sorts map
	// keys, but keeping this explicit documents the canonical hash contract.
	for _, row := range out {
		keys := make([]string, 0, len(row))
		for k := range row {
			keys = append(keys, k)
		}
		sort.Strings(keys)
	}
	return out, nil
}
