// Package jobqueue owns CloudNode execution queue naming and JetStream adapters.
package jobqueue

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// ConsumerName returns a durable consumer name for a specific executable route.
func ConsumerName(spaceID, codePackageID, jobType string) string {
	identity := fmt.Sprintf("%d:%s%d:%s%d:%s", len(spaceID), spaceID, len(codePackageID), codePackageID, len(jobType), jobType)
	sum := sha256.Sum256([]byte(identity))
	return "cn_exec_" + hex.EncodeToString(sum[:])[:24]
}
