package storageio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/pipeline"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func (c *Client) WriteQualityEvents(ctx context.Context, datasetID string, row marketdata.ResolvedKline, events []pipeline.QualityEvent) error {
	binding, err := c.binding(datasetID, RoleQualityEvent)
	if err != nil {
		return err
	}
	rows := make([]*storagepb.RecordRow, 0, len(events))
	for index, event := range events {
		providers, err := json.Marshal(event.ProviderIDs)
		if err != nil {
			return err
		}
		identity := fmt.Sprintf("%s|%s|%s|%s|%d|%s|%d", row.SubjectID, row.Frequency, row.DataTime.Format(timeFormat), event.Type, row.Revision, event.Reason, index)
		sum := sha256.Sum256([]byte(identity))
		rows = append(rows, &storagepb.RecordRow{
			Key: &storagepb.RecordKey{SpaceId: binding.SpaceID, DatasetId: binding.DatasetID, RecordId: hex.EncodeToString(sum[:]), Version: strconv.FormatInt(row.Revision, 10)},
			Columns: []*storagepb.ColumnValue{
				stringColumn("subject_id", row.SubjectID), stringColumn("frequency", string(row.Frequency)), timeColumn("data_time", row.DataTime),
				stringColumn("event_type", event.Type), stringColumn("provider_ids", string(providers)), stringColumn("reason", event.Reason),
				stringColumn("selected_provider", string(row.ProviderID)), stringColumn("source_dataset_id", row.SourceDatasetID), intColumn("revision", row.Revision), timeColumn("resolved_at", row.ResolvedAt),
			},
		})
	}
	if len(rows) == 0 {
		return nil
	}
	rsp, err := c.access.WriteRecordRows(ctx, &storagepb.WriteRecordRowsReq{AuthInfo: c.auth, Rows: rows, WriteMode: storagepb.RowWriteMode_ROW_WRITE_MODE_REPLACE})
	if err != nil {
		return err
	}
	return ensureOK("write quality events", rsp.GetRetInfo())
}

const timeFormat = "2006-01-02T15:04:05.999999999Z07:00"
