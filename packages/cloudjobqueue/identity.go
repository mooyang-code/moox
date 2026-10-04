// Package cloudjobqueue owns the stable identity of CloudNode job execution queues.
package cloudjobqueue

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

type Identity struct {
	SpaceID string
	JobType string
}

func (i Identity) ConsumerName() (string, error) {
	if err := validateIdentity(i.SpaceID, i.JobType); err != nil {
		return "", err
	}
	identity := fmt.Sprintf("%d:%s%d:%s", len(i.SpaceID), i.SpaceID, len(i.JobType), i.JobType)
	sum := sha256.Sum256([]byte(identity))
	return "cn_exec_" + hex.EncodeToString(sum[:])[:24], nil
}

func (i Identity) SubjectID() (string, error) {
	if err := validateIdentity(i.SpaceID, i.JobType); err != nil {
		return "", err
	}
	return i.JobType, nil
}

func validateIdentity(spaceID, jobType string) error {
	if err := validateField("space_id", spaceID); err != nil {
		return err
	}
	return validateField("job_type", jobType)
}

func validateField(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must not contain surrounding whitespace", name)
	}
	return nil
}
