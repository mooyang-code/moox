package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRecordIngestUpdatesBoundedTelemetry(t *testing.T) {
	before := testutil.ToFloat64(ingestTotal.WithLabelValues("success", ""))
	recordIngest(nil, "success", time.Now().Add(-time.Second))
	if got := testutil.ToFloat64(ingestTotal.WithLabelValues("success", "")); got != before+1 {
		t.Fatalf("success total = %v", got)
	}
	if testutil.ToFloat64(ingestLastSuccess) <= 0 {
		t.Fatal("last success was not recorded")
	}
}
