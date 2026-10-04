package metadata

import (
	"fmt"
	"strings"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

var immutableDatasetOwnerAttributeKeys = []string{
	"dataset_role", "owner_module", "write_owner", "source_dataset_id",
	"collector_task_id", "resample_task_id",
}

// PreserveDatasetOwnerAttributes prevents metadata updates from changing or
// removing the ownership markers that guard writes, markers, and deletion.
func PreserveDatasetOwnerAttributes(current, candidate *pb.Dataset) error {
	if current == nil || candidate == nil {
		return fmt.Errorf("dataset is required")
	}
	attrs := make(map[string]string, len(candidate.GetAttributes())+len(immutableDatasetOwnerAttributeKeys))
	for key, value := range candidate.GetAttributes() {
		attrs[key] = value
	}
	for _, key := range immutableDatasetOwnerAttributeKeys {
		currentValue := current.GetAttributes()[key]
		candidateValue := strings.TrimSpace(attrs[key])
		if candidateValue != "" && candidateValue != currentValue {
			return fmt.Errorf("dataset attribute %q is immutable", key)
		}
		if currentValue == "" {
			delete(attrs, key)
		} else {
			attrs[key] = currentValue
		}
	}
	candidate.Attributes = attrs
	return nil
}
