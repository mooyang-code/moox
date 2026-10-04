package view

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ReplayPendingSubjects discards leftover rebuild journals written by older
// binaries. Subject-ready publishing has been removed.
func (s *Service) ReplayPendingSubjects(ctx context.Context) error {
	if s == nil || strings.TrimSpace(s.pendingSubjectsDir) == "" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := os.ReadDir(s.pendingSubjectsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.RemoveAll(filepath.Join(s.pendingSubjectsDir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}
