package view

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func publishesSourceSubjectReady(view viewRef) bool {
	// Host/service metrics have no Factor consumer.
	return view.spaceID != "mooxsys"
}

// ReplayPendingSubjects discards leftover rebuild journals written by older
// binaries. Rebuilds never emit ViewSourceSubjectReady; live writes against an
// active index publish directly.
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
