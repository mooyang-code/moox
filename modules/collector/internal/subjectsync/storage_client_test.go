package subjectsync

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

func TestStorageClientHostAuthPreservesMasterSecretDerivation(t *testing.T) {
	setSubjectSyncStorageConfig(t)
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON", "")
	require.NoError(t, os.Unsetenv("MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON"))
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "subject-sync-test-secret")
	storage, err := NewStorageClient(subjectGatewayFunc(func(context.Context, string, string, any, any) error { return nil }))
	require.NoError(t, err)
	require.Equal(t, "moox-collector", storage.auth.AppId)
	require.Equal(t, mooxsecurity.HMACSHA256Hex("subject-sync-test-secret", []byte("moox-collector")), storage.auth.AppKey)
	storage.client = &fakeMetadataClient{listTags: func(_ context.Context, req *storagepb.ListTagsReq) (*storagepb.ListTagsRsp, error) {
		require.Equal(t, storage.auth, req.AuthInfo)
		return &storagepb.ListTagsRsp{RetInfo: storageSuccess()}, nil
	}}
	_, err = storage.ListTags(context.Background())
	require.NoError(t, err)
}

func TestStorageClientManagedAuthErrorsBeforeProxyCreation(t *testing.T) {
	setSubjectSyncStorageConfig(t)
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "subject-sync-test-secret")
	proxyCalls := 0
	gateway := subjectGatewayFunc(func(context.Context, string, string, any, any) error { proxyCalls++; return nil })
	for _, raw := range []string{"", "null", `{"moox-collector":"` + strings.Repeat("a", 64) + `","moox-collector":"` + strings.Repeat("b", 64) + `"}`} {
		t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON", raw)
		storage, err := NewStorageClient(gateway)
		require.Error(t, err)
		require.Nil(t, storage)
		require.NotContains(t, err.Error(), "subject-sync-test-secret")
	}
	require.Zero(t, proxyCalls)
}

func setSubjectSyncStorageConfig(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "binance.yaml")
	require.NoError(t, os.WriteFile(path, []byte("storage:\n  bindings:\n    spot:\n      auth_info:\n        app_id: moox-collector\n        app_key: host-config-key\n"), 0600))
	t.Setenv("MOOX_STORAGE_MARKET_CONFIG", path)
}

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

type subjectGatewayFunc func(context.Context, string, string, any, any) error

func (f subjectGatewayFunc) Invoke(ctx context.Context, service, method string, request, response any) error {
	return f(ctx, service, method, request, response)
}
