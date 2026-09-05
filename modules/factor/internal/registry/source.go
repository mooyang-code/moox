package registry

import (
	"crypto/sha1"
	"fmt"
	"strings"
)

// ResultDataset returns the managed factor result Dataset for one source object.
// Callers that have a source View should pass that View ID so the result keeps
// the business Space and frequency instead of inheriting a provider-specific
// primary Dataset ID.
func ResultDataset(sourceDatasetID string) string {
	return resultObjectID(withDatasetPrefix(sourceDatasetID), "_factor", 50)
}

// ResultView returns the managed factor result View for one source object.
func ResultView(sourceDatasetID string) string {
	return "view_" + resultObjectID(datasetBaseID(sourceDatasetID), "_factor", 50)
}

// ResultDatasetForView derives the result Dataset from the source View. This
// keeps the generated ID aligned with the user's query identity even when the
// View's primary Dataset is owned by a specific provider.
func ResultDatasetForView(sourceDatasetID, sourceViewID string) string {
	if strings.TrimSpace(sourceViewID) != "" {
		return ResultDataset(sourceViewID)
	}
	return ResultDataset(sourceDatasetID)
}

// ResultViewForView is the View counterpart of ResultDatasetForView.
func ResultViewForView(sourceDatasetID, sourceViewID string) string {
	if strings.TrimSpace(sourceViewID) != "" {
		return ResultView(sourceViewID)
	}
	return ResultView(sourceDatasetID)
}

func datasetBaseID(sourceDatasetID string) string {
	return strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(sourceDatasetID)), "view_"), "dataset_")
}

func withDatasetPrefix(sourceDatasetID string) string {
	return "dataset_" + datasetBaseID(sourceDatasetID)
}

func resultObjectID(sourceID, objectSuffix string, maxLen int) string {
	normalized := strings.ToLower(strings.TrimSpace(sourceID))
	candidate := normalized + objectSuffix
	if len(candidate) <= maxLen {
		return candidate
	}
	sum := sha1.Sum([]byte(normalized + "\x00" + objectSuffix))
	suffix := fmt.Sprintf("_%x", sum[:8])
	prefixLen := maxLen - len(suffix)
	prefix := strings.TrimRight(normalized, "_")
	if len(prefix) > prefixLen {
		prefix = strings.TrimRight(prefix[:prefixLen], "_")
	}
	if prefix == "" {
		prefix = strings.Repeat("d", prefixLen)
	}
	return prefix + suffix
}

// OutputField returns the stable physical field owned by one factor output.
func OutputField(factorID, output string) string {
	factorID = strings.ToLower(strings.TrimSpace(factorID))
	output = strings.ToLower(strings.TrimSpace(output))
	return factorID + "__" + output
}
