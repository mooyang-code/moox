package marketstorage

import (
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func validStorageExpectation(period time.Time) *storagepb.DatasetPeriodExpectation {
	return &storagepb.DatasetPeriodExpectation{
		SpaceId: "crypto", DatasetId: "bars", Frequency: "1m", PeriodTime: period.Unix(),
		SeriesHash: "abc", ExpectedCount: 2, DeadlineAt: period.Add(time.Minute).Unix(),
	}
}

func storageSuccess() *storagepb.RetInfo {
	return &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}
}
