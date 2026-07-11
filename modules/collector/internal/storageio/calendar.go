package storageio

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func (c *Client) WriteCalendarDays(ctx context.Context, datasetID string, generation time.Time, days []markets.CalendarDay) error {
	binding, err := c.binding(datasetID, RoleUnifiedData)
	if err != nil {
		return err
	}
	if binding.Feed != "calendar" {
		return fmt.Errorf("dataset %q is not calendar", datasetID)
	}
	if generation.IsZero() {
		return fmt.Errorf("calendar generation is required")
	}
	rows := make([]*storagepb.RecordRow, 0, len(days))
	for _, day := range days {
		if day.ExchangeID == "" || day.TradeDate == "" || len(day.Sessions) == 0 {
			return fmt.Errorf("calendar exchange, trade date and sessions are required")
		}
		rawSessions, err := json.Marshal(day.Sessions)
		if err != nil {
			return err
		}
		rows = append(rows, &storagepb.RecordRow{Key: &storagepb.RecordKey{SpaceId: binding.SpaceID, DatasetId: binding.DatasetID, RecordId: string(day.ExchangeID) + "|" + day.TradeDate, Version: generation.UTC().Format(time.RFC3339Nano)}, Columns: []*storagepb.ColumnValue{stringColumn("exchange_id", string(day.ExchangeID)), stringColumn("trade_date", day.TradeDate), stringColumn("timezone", day.Timezone), stringColumn("session_status", day.Status), stringColumn("sessions_json", string(rawSessions)), timeColumn("open_time", day.Sessions[0].Open), timeColumn("close_time", day.Sessions[len(day.Sessions)-1].Close), timeColumn("generation", generation)}})
	}
	rsp, err := c.access.WriteRecordRows(ctx, &storagepb.WriteRecordRowsReq{AuthInfo: c.auth, Rows: rows, WriteMode: storagepb.RowWriteMode_ROW_WRITE_MODE_REPLACE})
	if err != nil {
		return err
	}
	return ensureOK("write calendar days", rsp.GetRetInfo())
}
