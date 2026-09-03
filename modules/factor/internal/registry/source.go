package registry

import (
	"crypto/sha1"
	"fmt"
	"strings"
)

// ResultDataset returns the managed factor result Dataset for one source Dataset.
func ResultDataset(sourceDatasetID string) string {
	return resultObjectID(withDatasetPrefix(sourceDatasetID), "_factor", 50)
}

// ResultView returns the managed factor result View for one source Dataset.
func ResultView(sourceDatasetID string) string {
	return "view_" + resultObjectID(datasetBaseID(sourceDatasetID), "_factor_v", 25)
}

// ResultDatasetForView keeps the readable Dataset-based name for the usual
// view_<dataset> pairing, while preserving uniqueness when multiple Views
// share one primary Dataset.
func ResultDatasetForView(sourceDatasetID, sourceViewID string) string {
	if canonicalSourceView(sourceDatasetID, sourceViewID) {
		return ResultDataset(sourceDatasetID)
	}
	return resultObjectID(withDatasetPrefix(withViewDiscriminator(sourceDatasetID, sourceViewID)), "_factor", 50)
}

// ResultViewForView is the View counterpart of ResultDatasetForView.
func ResultViewForView(sourceDatasetID, sourceViewID string) string {
	if canonicalSourceView(sourceDatasetID, sourceViewID) {
		return ResultView(sourceDatasetID)
	}
	return "view_" + resultObjectID(withViewDiscriminator(datasetBaseID(sourceDatasetID), sourceViewID), "_factor_v", 25)
}

func datasetBaseID(sourceDatasetID string) string {
	return strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(sourceDatasetID)), "view_"), "dataset_")
}

func withDatasetPrefix(sourceDatasetID string) string {
	return "dataset_" + datasetBaseID(sourceDatasetID)
}

func canonicalSourceView(sourceDatasetID, sourceViewID string) bool {
	viewID := strings.ToLower(strings.TrimSpace(sourceViewID))
	return viewID == "" || viewID == "view_"+datasetBaseID(sourceDatasetID)
}

func withViewDiscriminator(sourceDatasetID, sourceViewID string) string {
	sum := sha1.Sum([]byte(strings.ToLower(strings.TrimSpace(sourceViewID))))
	return strings.TrimRight(datasetBaseID(sourceDatasetID), "_") + fmt.Sprintf("_%x", sum[:4])
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
