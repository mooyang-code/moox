package subjectsync

import (
	"context"
	"testing"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/client"
)

type fakeMetadataClient struct {
	listTags func(context.Context, *storagepb.ListTagsReq) (*storagepb.ListTagsRsp, error)
}

func (f *fakeMetadataClient) ListTags(ctx context.Context, req *storagepb.ListTagsReq, _ ...client.Option) (*storagepb.ListTagsRsp, error) {
	return f.listTags(ctx, req)
}

func (*fakeMetadataClient) ApplyTagSnapshot(context.Context, *storagepb.ApplyTagSnapshotReq, ...client.Option) (*storagepb.ApplyTagSnapshotRsp, error) {
	return nil, nil
}

func (*fakeMetadataClient) ReportTagRunFailure(context.Context, *storagepb.ReportTagRunFailureReq, ...client.Option) (*storagepb.ReportTagRunFailureRsp, error) {
	return nil, nil
}

func (*fakeMetadataClient) UpdateSubjectAttributes(context.Context, *storagepb.UpdateSubjectAttributesReq, ...client.Option) (*storagepb.UpdateSubjectAttributesRsp, error) {
	return nil, nil
}

func (*fakeMetadataClient) GetDataSource(context.Context, *storagepb.GetDataSourceReq, ...client.Option) (*storagepb.GetDataSourceRsp, error) {
	return nil, nil
}

func (*fakeMetadataClient) UpdateDataSource(context.Context, *storagepb.UpdateDataSourceReq, ...client.Option) (*storagepb.UpdateDataSourceRsp, error) {
	return nil, nil
}

func TestStorageClientListTagsContinuesWhenPageIsFull(t *testing.T) {
	called := 0
	client := &fakeMetadataClient{listTags: func(_ context.Context, req *storagepb.ListTagsReq) (*storagepb.ListTagsRsp, error) {
		called++
		if req.GetPage().GetPage() == 1 {
			tags := make([]*storagepb.Tag, storagePageSize)
			for i := range tags {
				tags[i] = &storagepb.Tag{TagId: "tag"}
			}
			return &storagepb.ListTagsRsp{
				RetInfo: storageSuccess(),
				Tags:    tags,
				PageResult: &storagepb.PageResult{
					Page:  1,
					Size:  storagePageSize,
					Total: storagePageSize + 1,
				},
			}, nil
		}
		return &storagepb.ListTagsRsp{
			RetInfo:    storageSuccess(),
			Tags:       []*storagepb.Tag{{TagId: "last"}},
			PageResult: &storagepb.PageResult{Page: 2, Size: storagePageSize, Total: storagePageSize + 1},
		}, nil
	}}

	storage := &StorageClient{client: client, auth: &storagepb.AuthInfo{AppId: "test"}}
	tags, err := storage.ListTags(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if called != 2 || len(tags) != storagePageSize+1 || tags[len(tags)-1].GetTagId() != "last" {
		t.Fatalf("calls=%d tags=%d last=%q", called, len(tags), tags[len(tags)-1].GetTagId())
	}
}

func TestStorageClientListTagsStopsAtExactFullPage(t *testing.T) {
	called := 0
	storage := &StorageClient{
		client: &fakeMetadataClient{listTags: func(_ context.Context, req *storagepb.ListTagsReq) (*storagepb.ListTagsRsp, error) {
			called++
			if req.GetPage().GetPage() != 1 {
				t.Fatalf("unexpected page %d", req.GetPage().GetPage())
			}
			return &storagepb.ListTagsRsp{
				RetInfo: storageSuccess(), Tags: make([]*storagepb.Tag, storagePageSize),
				PageResult: &storagepb.PageResult{Page: 1, Size: storagePageSize, Total: storagePageSize},
			}, nil
		}},
		auth: &storagepb.AuthInfo{AppId: "test"},
	}

	tags, err := storage.ListTags(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if called != 1 || len(tags) != storagePageSize {
		t.Fatalf("calls=%d tags=%d", called, len(tags))
	}
}

func storageSuccess() *storagepb.RetInfo {
	return &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}
}
