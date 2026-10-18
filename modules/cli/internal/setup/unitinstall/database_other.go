//go:build !linux

package unitinstall

import (
	"context"
	"errors"
	"slices"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
)

func initializeCandidateDatabase(_ context.Context, _ *unitruntime.Maintenance, candidate Prepared) error {
	if slices.Contains(candidate.Components, "storage-primary") {
		return errors.New("Storage activation requires Linux")
	}
	return nil
}
