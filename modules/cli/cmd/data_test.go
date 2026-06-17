package cmd

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestDataCommandsRequireStorageURLAndDoNotExposeLocalStorageRoot(t *testing.T) {
	for _, cmd := range []*cobra.Command{dataCSVImportCmd, dataRowsExportCmd} {
		if flag := cmd.Flags().Lookup("storage-root"); flag != nil {
			t.Fatalf("%s exposes deprecated --storage-root flag", cmd.CommandPath())
		}
		if flag := cmd.Flags().Lookup("storage-url"); flag == nil {
			t.Fatalf("%s must expose --storage-url", cmd.CommandPath())
		}
	}
}
