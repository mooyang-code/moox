package outbox

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var testMarkerSeq atomic.Int64

// appendTestMarker 追加一条写入方上报的周期完成标记，在 Outbox 里留下一条待转发的事件；每次调用的触发事件不同。
func appendTestMarker(ctx context.Context, store *pebble.Store, spaceID, datasetID string) (string, error) {
	n := testMarkerSeq.Add(1)
	raw, _, err := pebble.BuildProducerPeriodCompletedMessage(spaceID, &pb.DatasetPeriodCompletedMarker{
		DatasetId: datasetID, Frequency: "1m", PeriodTime: 1786032000, Status: "complete",
		CauseEventId: fmt.Sprintf("cause-%d", n), CompletedAt: timestamppb.New(time.Unix(1786032060, 0)),
	})
	if err != nil {
		return "", err
	}
	return store.AppendDatasetMarker(ctx, raw)
}
