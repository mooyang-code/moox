package primarystore

import (
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestIsFactorHistoryReadAcceptsFactorAppIDs(t *testing.T) {
	for _, appID := range []string{"factor", "moox-factor"} {
		if !isFactorHistoryRead(&pb.ReadTimeSeriesRowsReq{AuthInfo: &pb.AuthInfo{AppId: appID}}) {
			t.Fatalf("app id %q must be routed through factor history reads", appID)
		}
	}
	if isFactorHistoryRead(&pb.ReadTimeSeriesRowsReq{AuthInfo: &pb.AuthInfo{AppId: "collector"}}) {
		t.Fatal("collector must not use factor history reads")
	}
	if isFactorHistoryRead(&pb.ReadTimeSeriesRowsReq{AuthInfo: &pb.AuthInfo{AppId: "factor"}, Keys: []*pb.TimeSeriesKey{{}}}) {
		t.Fatal("exact key reads must not use factor history reads")
	}
}
