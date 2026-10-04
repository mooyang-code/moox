package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// SetID derives the canonical FactorSet ID from its source and frequency.
func SetID(sourceDatasetID, freq string) string {
	return "fset_" + sourceSuffix(sourceDatasetID, freq)
}

// ResultDatasetID derives the canonical result Dataset ID for a FactorSet.
func ResultDatasetID(sourceDatasetID, freq string) string {
	return "dataset_factor_" + sourceSuffix(sourceDatasetID, freq)
}

// SourceHash returns the content identity used for immutable factor source files.
func SourceHash(sourceCode string) string {
	hash := sha256.Sum256([]byte(sourceCode))
	return "sha256:" + hex.EncodeToString(hash[:])
}

func sourceSuffix(sourceDatasetID, freq string) string {
	suffix := strings.TrimPrefix(strings.TrimSpace(sourceDatasetID), "dataset_")
	freq = strings.TrimSpace(freq)
	if freq != "" && !strings.HasSuffix(suffix, "_"+freq) {
		suffix += "_" + freq
	}
	return suffix
}
