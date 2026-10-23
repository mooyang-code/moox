package command

import (
	"bytes"
	"context"
	"testing"

	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/stretchr/testify/require"
)

func TestResampleCommandsValidateRequiredFlagsBeforeConnecting(t *testing.T) {
	ctx := context.Background()
	var out bytes.Buffer
	require.ErrorContains(t, runResampleBackfill(ctx, &out, resampleFlags{}), "--space-id and --task")
	require.ErrorContains(t, runResampleBackfill(ctx, &out, resampleFlags{klineRangeFlags: klineRangeFlags{SpaceID: "crypto"}, TaskID: "t"}), "--from is required")
	require.ErrorContains(t, runResampleBackfill(ctx, &out, resampleFlags{klineRangeFlags: klineRangeFlags{SpaceID: "crypto", From: "2026-10-01"}, TaskID: "t"}), "--to is required")
	require.ErrorContains(t, runResampleJobs(ctx, &out, resampleFlags{}), "--space-id is required")
	require.ErrorContains(t, runResampleCancel(ctx, &out, resampleFlags{klineRangeFlags: klineRangeFlags{SpaceID: "crypto"}}), "--job-id")
}

func TestResampleJobMapExposesProgressAndSkippedSamples(t *testing.T) {
	job := resampleJobMap(&collectorpb.ResampleJob{JobId: "j", TaskId: "t", Status: "running", TotalBuckets: 96, DoneBuckets: 40, Written: 79, Skipped: 1, SkippedSamples: []string{"ETH@2026-10-20T09:45:00Z：source window requires 9 rows, got 8"}})
	require.Equal(t, "running", job["status"])
	require.EqualValues(t, 96, job["buckets_total"])
	require.EqualValues(t, 40, job["buckets_done"])
	require.EqualValues(t, 1, job["skipped"])
	require.Len(t, job["skipped_samples"], 1)
	require.Nil(t, resampleJobMap(nil))
	require.NotEqual(t, newResampleRequestID(), newResampleRequestID())
}
