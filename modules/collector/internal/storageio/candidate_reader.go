package storageio

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func (c *Client) ReadCandidates(ctx context.Context, subjectID string, frequency marketdata.Frequency, dataTime time.Time) ([]*storagepb.TimeSeriesRow, error) {
	keys := make([]*storagepb.TimeSeriesKey, 0)
	for _, binding := range c.bindings {
		if binding.Role != RoleProviderData || binding.Feed != "kline" {
			continue
		}
		keys = append(keys, &storagepb.TimeSeriesKey{SpaceId: binding.SpaceID, DatasetId: binding.DatasetID, SubjectId: subjectID, Freq: string(frequency), DataTime: dataTime.UTC().Format(time.RFC3339)})
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no provider kline dataset binding")
	}
	rsp, err := c.access.ReadTimeSeriesRows(ctx, &storagepb.ReadTimeSeriesRowsReq{AuthInfo: c.auth, Keys: keys, TimeRange: &storagepb.TimeRange{StartTime: dataTime.UTC().Format(time.RFC3339), EndTime: dataTime.UTC().Format(time.RFC3339)}})
	if err != nil {
		return nil, err
	}
	if err := ensureOK("read source candidates", rsp.GetRetInfo()); err != nil {
		return nil, err
	}
	return rsp.GetRows(), nil
}
