package schema

import (
	"os"
	"strings"
	"testing"
)

func TestMetadataSchemaV4Contract(t *testing.T) {
	sql, err := os.ReadFile("metadata.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(sql)
	for _, want := range []string{
		"VALUES ('schema_version', '4')",
		"c_data_node_id",
		"c_keep_duration TEXT NOT NULL DEFAULT '0'",
		"c_active_slot",
		"c_new_slot",
		"c_backfilled_rows",
		"c_safe_error",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("metadata schema missing %q", want)
		}
	}
	for _, forbidden := range []string{"c_keep_duration", "c_content_hash", "c_required"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("metadata schema contains removed column %q", forbidden)
		}
	}
}
