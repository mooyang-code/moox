package metrics

import (
	"errors"
	"strings"
)

// InternalMetricSpaceID is the only Storage Space used by application
// metrics. Catalog rows are discovered from trusted reports, not user-created
// records, so every API and ingestion path must agree on this scope.
const InternalMetricSpaceID = "mooxsys"

var ErrMetricSpaceScope = errors.New("metrics are scoped to internal space mooxsys")

func ValidateMetricSpace(spaceID string) error {
	if strings.TrimSpace(spaceID) != InternalMetricSpaceID {
		return ErrMetricSpaceScope
	}
	return nil
}
