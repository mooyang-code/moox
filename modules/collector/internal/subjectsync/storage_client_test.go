package subjectsync

import (
	"context"
	"testing"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

// 构造只要网关；请求原样发给 Storage，不需要任何存储口令配置。
func TestStorageClientRequiresOnlyGateway(t *testing.T) {
	_, err := NewStorageClient(nil)
	require.Error(t, err)
	storage, err := NewStorageClient(subjectGatewayFunc(func(context.Context, string, string, any, any) error { return nil }))
	require.NoError(t, err)
	storage.client = &fakeMetadataClient{listTags: func(_ context.Context, req *storagepb.ListTagsReq) (*storagepb.ListTagsRsp, error) {
		return &storagepb.ListTagsRsp{RetInfo: storageSuccess()}, nil
	}}
	_, err = storage.ListTags(context.Background())
	require.NoError(t, err)
}

type fakeMetadataClient struct {
	listTags    func(context.Context, *storagepb.ListTagsReq) (*storagepb.ListTagsRsp, error)
	listMembers func(context.Context, *storagepb.ListTagMembersReq) (*storagepb.ListTagMembersRsp, error)
}

func (f *fakeMetadataClient) ListTags(ctx context.Context, req *storagepb.ListTagsReq, _ ...client.Option) (*storagepb.ListTagsRsp, error) {
	return f.listTags(ctx, req)
}

func (*fakeMetadataClient) ApplyTagSnapshot(context.Context, *storagepb.ApplyTagSnapshotReq, ...client.Option) (*storagepb.ApplyTagSnapshotRsp, error) {
	return nil, nil
}

func (*fakeMetadataClient) UpdateSubjectAttributes(context.Context, *storagepb.UpdateSubjectAttributesReq, ...client.Option) (*storagepb.UpdateSubjectAttributesRsp, error) {
	return nil, nil
}

func (f *fakeMetadataClient) ListTagMembers(ctx context.Context, req *storagepb.ListTagMembersReq, _ ...client.Option) (*storagepb.ListTagMembersRsp, error) {
	if f.listMembers == nil {
		return &storagepb.ListTagMembersRsp{RetInfo: storageSuccess()}, nil
	}
	return f.listMembers(ctx, req)
}

func (*fakeMetadataClient) SetTagMemberTradingStatus(context.Context, *storagepb.SetTagMemberTradingStatusReq, ...client.Option) (*storagepb.SetTagMemberTradingStatusRsp, error) {
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

	storage := &StorageClient{client: client}
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

type subjectGatewayFunc func(context.Context, string, string, any, any) error

func (f subjectGatewayFunc) Invoke(ctx context.Context, service, method string, request, response any) error {
	return f(ctx, service, method, request, response)
}

// 成员列表按页读完：满页就接着读下一页。
func TestListTagMembersReadsAllPages(t *testing.T) {
	pages := 0
	storage := &StorageClient{client: &fakeMetadataClient{listMembers: func(_ context.Context, req *storagepb.ListTagMembersReq) (*storagepb.ListTagMembersRsp, error) {
		pages++
		if req.GetStatus() != "active" {
			t.Fatalf("status filter = %q", req.GetStatus())
		}
		size := storagePageSize
		if req.GetPage().GetPage() == 2 {
			size = 3
		}
		members := make([]*storagepb.TagMember, size)
		for i := range members {
			members[i] = &storagepb.TagMember{Subject: &storagepb.Subject{SubjectId: "S"}}
		}
		return &storagepb.ListTagMembersRsp{RetInfo: storageSuccess(), Members: members, PageResult: &storagepb.PageResult{HasMore: req.GetPage().GetPage() == 1}}, nil
	}}}
	members, err := storage.ListTagMembers(context.Background(), "crypto", "binance_spot", "active")
	if err != nil {
		t.Fatal(err)
	}
	if pages != 2 || len(members) != storagePageSize+3 {
		t.Fatalf("pages=%d members=%d", pages, len(members))
	}
}
