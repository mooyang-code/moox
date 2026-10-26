package metrics

import (
	"context"
	"testing"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

type fakeMetadata struct {
	subjects     []*storagepb.DatasetSubject
	dataset      *storagepb.Dataset
	subjectCalls int
	datasetCalls int
}

func (f *fakeMetadata) GetDataset(context.Context, *storagepb.GetDatasetReq, ...client.Option) (*storagepb.GetDatasetRsp, error) {
	f.datasetCalls++
	return &storagepb.GetDatasetRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, Dataset: f.dataset}, nil
}
func (f *fakeMetadata) ListDatasets(context.Context, *storagepb.ListDatasetsReq, ...client.Option) (*storagepb.ListDatasetsRsp, error) {
	return &storagepb.ListDatasetsRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}}, nil
}
func (f *fakeMetadata) ListDatasetSubjects(context.Context, *storagepb.ListDatasetSubjectsReq, ...client.Option) (*storagepb.ListDatasetSubjectsRsp, error) {
	f.subjectCalls++
	return &storagepb.ListDatasetSubjectsRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}, DatasetSubjects: f.subjects}, nil
}
func (f *fakeMetadata) ListTags(context.Context, *storagepb.ListTagsReq, ...client.Option) (*storagepb.ListTagsRsp, error) {
	return &storagepb.ListTagsRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}}, nil
}

func TestStorageAdapterListsAndCachesActiveDatasetSubjects(t *testing.T) {
	f := &fakeMetadata{subjects: []*storagepb.DatasetSubject{
		{SubjectId: "BTC", Status: "active"},
		{SubjectId: "OLD", Status: "archived"},
		{SubjectId: "", Status: "active"},
	}}
	adapter := NewStorageAdapter(f)
	first, err := adapter.ListActiveDatasetSubjects(context.Background(), "crypto", "dataset")
	require.NoError(t, err)
	assert.Equal(t, map[string]struct{}{"BTC": {}}, first)
	second, err := adapter.ListActiveDatasetSubjects(context.Background(), "crypto", "dataset")
	require.NoError(t, err)
	assert.Equal(t, first, second)
	assert.Equal(t, 1, f.subjectCalls)
}

func TestStorageAdapterCachesDatasetDisplayName(t *testing.T) {
	f := &fakeMetadata{dataset: &storagepb.Dataset{Name: " 现货小时K线 "}}
	adapter := NewStorageAdapter(f)
	assert.Equal(t, "现货小时K线", adapter.DatasetDisplayName(context.Background(), "crypto", "dataset"))
	assert.Equal(t, "现货小时K线", adapter.DatasetDisplayName(context.Background(), "crypto", "dataset"))
	assert.Equal(t, 1, f.datasetCalls)
	assert.Empty(t, adapter.DatasetDisplayName(context.Background(), "crypto", " "))
}

func TestStorageHelpers(t *testing.T) {
	assert.True(t, isActive(" active "))
	assert.False(t, isActive("deleted"))
	require.NoError(t, storageOK("action", &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}))
	require.Error(t, storageOK("action", &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: "fail"}))
}
