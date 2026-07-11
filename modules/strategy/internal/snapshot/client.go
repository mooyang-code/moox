package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"sort"
)

type Input struct {
	Data             []map[string]any
	Revision, Cutoff string
}

func Normalize(in Input) ([]map[string]any, string, error) {
	for _, row := range in.Data {
		keys := make([]string, 0, len(row))
		for k := range row {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		_ = keys
	}
	b, err := json.Marshal(in)
	if err != nil {
		return nil, "", err
	}
	h := sha256.Sum256(b)
	return in.Data, hex.EncodeToString(h[:]), nil
}
func ValidateOutput(o domain.Output) error {
	if o.Action == "" || o.NextState == nil {
		return &SnapshotError{"invalid strategy snapshot output"}
	}
	return nil
}

type SnapshotError struct{ Message string }

func (e *SnapshotError) Error() string { return e.Message }
