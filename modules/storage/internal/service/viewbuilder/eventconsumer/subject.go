package eventconsumer

import (
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/packages/jetstream"
)

const DatasetFieldsChangedSubjectPrefix = "moox.storage.fields_changed.v1"

func DatasetFieldsChangedSubject(prefix, spaceID, datasetID string) (string, error) {
	if strings.TrimSpace(prefix) == "" {
		prefix = DatasetFieldsChangedSubjectPrefix
	}
	spaceToken, err := jetstream.EncodeSubjectToken(spaceID)
	if err != nil {
		return "", fmt.Errorf("encode space_id: %w", err)
	}
	datasetToken, err := jetstream.EncodeSubjectToken(datasetID)
	if err != nil {
		return "", fmt.Errorf("encode dataset_id: %w", err)
	}
	return fmt.Sprintf("%s.%s.%s", strings.TrimSuffix(prefix, "."), spaceToken, datasetToken), nil
}

func ParseDatasetFieldsChangedSubject(prefix, subject string) (string, string, error) {
	if strings.TrimSpace(prefix) == "" {
		prefix = DatasetFieldsChangedSubjectPrefix
	}
	parts := strings.Split(subject, ".")
	prefixParts := strings.Split(strings.TrimSuffix(prefix, "."), ".")
	if len(parts) != len(prefixParts)+2 || strings.Join(parts[:len(prefixParts)], ".") != strings.TrimSuffix(prefix, ".") {
		return "", "", fmt.Errorf("invalid dataset fields subject %q", subject)
	}
	spaceID, err := jetstream.DecodeSubjectToken(parts[len(prefixParts)])
	if err != nil {
		return "", "", fmt.Errorf("decode space token: %w", err)
	}
	datasetID, err := jetstream.DecodeSubjectToken(parts[len(prefixParts)+1])
	if err != nil {
		return "", "", fmt.Errorf("decode dataset token: %w", err)
	}
	return spaceID, datasetID, nil
}
