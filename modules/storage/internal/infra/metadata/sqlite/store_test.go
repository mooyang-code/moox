package sqlite

import "testing"

func TestMetadataSchemaVersionCompatible(t *testing.T) {
	for _, version := range []string{"2", "3"} {
		if !metadataSchemaVersionCompatible(version) {
			t.Fatalf("metadata schema version %s should be compatible", version)
		}
	}
	for _, version := range []string{"", "1", "4"} {
		if metadataSchemaVersionCompatible(version) {
			t.Fatalf("metadata schema version %s should not be compatible", version)
		}
	}
}
