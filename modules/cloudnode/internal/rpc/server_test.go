package rpc

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/cloudcredential"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/config"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRetHelpers_ShouldMapCodes(t *testing.T) {
	ok := retOK()
	assert.Equal(t, pb.ErrorCode_SUCCESS, ok.GetCode())
	assert.Equal(t, "ok", ok.GetMsg())

	err := retErr(pb.ErrorCode_INVALID_PARAM, "bad")
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, err.GetCode())
	assert.Equal(t, "bad", err.GetMsg())
}

func TestRetFromError_ShouldMapKnownErrors(t *testing.T) {
	cases := []struct {
		err  error
		code pb.ErrorCode
		msg  string
	}{
		{store.ErrPollingNodeNotFound, pb.ErrorCode_NOT_FOUND, "cloud node not found"},
		{gorm.ErrRecordNotFound, pb.ErrorCode_NOT_FOUND, "resource not found"},
		{errors.New("boom"), pb.ErrorCode_INNER_ERR, "internal error"},
	}
	for _, tc := range cases {
		got := retFromError(tc.err)
		assert.Equal(t, tc.code, got.GetCode())
		assert.Equal(t, tc.msg, got.GetMsg())
	}
}

func TestNew_ShouldApplyOptions(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cloudnode.db")
	mgr, err := store.Open(&config.DatabaseConfig{Path: dbPath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })

	resolver := fakeCredentialResolver{}
	svc := New(mgr, WithCredentialResolver(resolver))
	require.NotNil(t, svc)
	assert.NotNil(t, svc.catalog)
	assert.Equal(t, resolver, svc.credentialResolver)
	assert.NotNil(t, svc.scfClientFactory)

	assert.NotNil(t, svc.scfClientFactory(cloudcredential.TencentCredential{SecretID: "id", SecretKey: "key"}))
}

func TestListCloudRegions_ShouldReturnStaticCatalog(t *testing.T) {
	svc := &Service{catalog: newCatalogForAccountTests(t)}
	rsp, err := svc.ListCloudRegions(context.Background(), &pb.ListCloudRegionsReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Len(t, rsp.GetRegions(), 18)
	assert.Equal(t, int64(18), rsp.GetTotal())
}

type fakeCredentialResolver struct {
	credential cloudcredential.TencentCredential
	err        error
}

func (r fakeCredentialResolver) Resolve(context.Context, store.CloudAccount) (cloudcredential.TencentCredential, error) {
	return r.credential, r.err
}
