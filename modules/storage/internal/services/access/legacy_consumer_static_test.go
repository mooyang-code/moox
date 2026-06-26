package access

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAccessServiceDoesNotContainLegacyEventConsumers(t *testing.T) {
	t.Parallel()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	productionFiles := []string{"service.go", "data.go"}
	for _, filename := range productionFiles {
		raw, err := os.ReadFile(filepath.Join(dir, filename))
		if err != nil {
			t.Fatalf("read %s failed: %v", filename, err)
		}
		content := string(raw)
		for _, legacy := range []string{
			"StartEventConsumers",
			"handleTimeSeriesRowsChangedForView",
			"handleRecordRowsChangedForSearch",
			"runSearchIndexWorker",
			"indexRecordRowsFromAccess",
			"recordRowsChangedSub",
			"timeSeriesRowsChangedSub",
			"indexJobs",
		} {
			if strings.Contains(content, legacy) {
				t.Fatalf("%s still contains legacy access-side event consumer %q", filename, legacy)
			}
		}
	}
}
