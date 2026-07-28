package registry

import (
	"crypto/sha1"
	"fmt"
	"strings"
)

// ResultDataset returns the default factor result dataset for one source dataset.
func ResultDataset(sourceDataset string) string {
	normalized := strings.ToLower(strings.TrimSpace(sourceDataset))
	candidate := normalized + "_factor"
	if len(candidate) <= 20 {
		return candidate
	}
	sum := sha1.Sum([]byte(normalized))
	suffix := fmt.Sprintf("_f%x", sum[:2])
	prefixLen := 20 - len(suffix)
	prefix := strings.TrimRight(normalized, "_")
	if len(prefix) > prefixLen {
		prefix = strings.TrimRight(prefix[:prefixLen], "_")
	}
	if prefix == "" {
		prefix = "dataset"
	}
	return prefix + suffix
}
