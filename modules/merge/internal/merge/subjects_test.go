package merge

import (
	"context"
	"testing"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

type fakeSubjectClient struct {
	pages []*storagepb.ListDatasetSubjectsRsp
	calls int
}

func (f *fakeSubjectClient) ListDatasetSubjects(_ context.Context, req *storagepb.ListDatasetSubjectsReq, _ ...client.Option) (*storagepb.ListDatasetSubjectsRsp, error) {
	f.calls++
	idx := int(req.GetPage().GetPage()) - 1
	if idx < 0 || idx >= len(f.pages) {
		return &storagepb.ListDatasetSubjectsRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}}, nil
	}
	return f.pages[idx], nil
}

func TestMetadataSubjectListerListsActiveCanonicalIDs(t *testing.T) {
	client := &fakeSubjectClient{pages: []*storagepb.ListDatasetSubjectsRsp{
		{
			RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
			DatasetSubjects: []*storagepb.DatasetSubject{
				{SubjectId: "BTC-USDT-SPOT", Status: "active"},
				{SubjectId: "ETH-USDT", Status: "disabled"},
				{SubjectId: "SOL-USDT", Status: "deleted"},
			},
			PageResult: &commonpb.PageResult{HasMore: true},
		},
		{
			RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
			DatasetSubjects: []*storagepb.DatasetSubject{
				{SubjectId: "BTC-USDT-SWAP", Status: "active"},
				{SubjectId: "", Status: "active"},
			},
		},
	}}
	lister := NewMetadataSubjectLister(client, &commonpb.AuthInfo{AppId: "moox-merge"})
	got, err := lister.ListActiveDatasetSubjects(context.Background(), "crypto", "dataset_binance_spot_kline_1m")
	require.NoError(t, err)
	require.Equal(t, []string{"BTC-USDT"}, got)
	require.Equal(t, 2, client.calls)
}
