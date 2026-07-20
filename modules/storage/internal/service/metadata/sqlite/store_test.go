package sqlite

import "testing"

func TestMetadataSchemaVersionIsExact(t *testing.T) {
	for _, version := range []string{"", "1", "2", "3", "5"} {
		if version == metadataSchemaVersion {
			t.Fatalf("test case %q unexpectedly equals current schema version", version)
		}
	}
	if metadataSchemaVersion != "4" {
		t.Fatalf("metadata schema version = %q, want 4", metadataSchemaVersion)
	}
}
