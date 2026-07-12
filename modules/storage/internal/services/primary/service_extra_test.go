package primary

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeletePrimaryRowsDeletesKeys(t *testing.T) {
	store := &primaryTestStore{}
	svc := NewService(Options{Pebble: store})
	defer svc.Close()

	keys := []*pb.PrimaryStoreKey{{SpaceId: "crypto", DatasetId: "kline", Key: "BTC|1m|_", Version: "2026-07-11T00:00:00Z"}}
	rsp, err := svc.DeletePrimaryRows(context.Background(), &pb.DeletePrimaryRowsReq{
		Target: &pb.PrimaryStoreTarget{NodeId: "node-1"},
		Keys:   keys,
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Equal(t, uint32(1), rsp.GetDeleted())
}

func TestDeletePrimaryRowsRejectsMissingKeys(t *testing.T) {
	svc := NewService(Options{Pebble: &primaryTestStore{}})
	defer svc.Close()
	rsp, err := svc.DeletePrimaryRows(context.Background(), &pb.DeletePrimaryRowsReq{Target: &pb.PrimaryStoreTarget{}})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestNormalizeScanPrimaryRowsReqClonesRequest(t *testing.T) {
	req := &pb.ScanPrimaryRowsReq{Target: &pb.PrimaryStoreTarget{SpaceId: "crypto"}}
	normalized := normalizeScanPrimaryRowsReq(req)
	assert.Equal(t, "crypto", normalized.GetTarget().GetSpaceId())
	assert.NotSame(t, req, normalized)
}

func TestNormalizeScanPrimaryRowsReqNilRequest(t *testing.T) {
	normalized := normalizeScanPrimaryRowsReq(nil)
	require.NotNil(t, normalized)
}

func TestValidateOutboxMessageRejectsEmptyPayload(t *testing.T) {
	msg := testOutboxMessage(t, "node-1")
	err := validateOutboxMessage(&pb.WritePrimaryRowsReq{
		Target:        &pb.PrimaryStoreTarget{NodeId: "node-1"},
		OutboxMessage: msg[:len(msg)-1],
	})
	require.Error(t, err)
}

func TestValidateOutboxMessageRejectsMissingTargetNode(t *testing.T) {
	msg := testOutboxMessage(t, "node-1")
	err := validateOutboxMessage(&pb.WritePrimaryRowsReq{OutboxMessage: msg})
	require.Error(t, err)
}

func TestServiceCloseHandlesNilService(t *testing.T) {
	var svc *Service
	assert.NoError(t, svc.Close())
}

func TestRetInfoErrorReturnsNilForSuccess(t *testing.T) {
	assert.NoError(t, retInfoError(&pb.RetInfo{Code: pb.ErrorCode_SUCCESS}))
}

func TestRetInfoErrorFormatsFailure(t *testing.T) {
	err := retInfoError(&pb.RetInfo{Code: pb.ErrorCode_INVALID_PARAM, Msg: "bad"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "INVALID_PARAM")
}
