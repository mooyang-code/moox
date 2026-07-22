package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mooyang-code/moox/modules/streamcalc/internal/aggregate"
)

type Store interface {
	Load(context.Context) (aggregate.Snapshot, error)
	Save(context.Context, aggregate.Snapshot) error
}

type FileStore struct{ Path string }

func (s FileStore) Load(ctx context.Context) (aggregate.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return aggregate.Snapshot{}, err
	}
	if s.Path == "" {
		return aggregate.Snapshot{}, fmt.Errorf("checkpoint path is required")
	}
	raw, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return aggregate.Snapshot{}, nil
	}
	if err != nil {
		return aggregate.Snapshot{}, err
	}
	var snapshot aggregate.Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return aggregate.Snapshot{}, fmt.Errorf("decode checkpoint: %w", err)
	}
	return snapshot, nil
}

func (s FileStore) Save(ctx context.Context, snapshot aggregate.Snapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.Path == "" {
		return fmt.Errorf("checkpoint path is required")
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encode checkpoint: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.Path), ".streamcalc-checkpoint-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.Path)
}
