package delist

import (
	"context"
	"errors"
	"testing"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type fakeInvoker struct {
	service, method string
	request         *adminpb.GetReadonlyExchangeKeyReq
	response        *adminpb.GetReadonlyExchangeKeyRsp
	err             error
}

func (f *fakeInvoker) Invoke(_ context.Context, service, method string, request, response any) error {
	f.service, f.method = service, method
	f.request, _ = request.(*adminpb.GetReadonlyExchangeKeyReq)
	if f.err != nil {
		return f.err
	}
	proto.Merge(response.(proto.Message), f.response)
	return nil
}

func TestGatewayKeysUseOnlyTheReadonlyKeyMethod(t *testing.T) {
	ok := &fakeInvoker{response: &adminpb.GetReadonlyExchangeKeyRsp{RetInfo: &adminpb.RetInfo{Code: adminpb.ErrorCode_SUCCESS}, KeyId: "api-key-public-part"}}
	key, err := (&GatewayKeys{Gateway: ok}).ReadonlyKey(context.Background())
	require.NoError(t, err)
	require.Equal(t, "api-key-public-part", key)
	require.Equal(t, "trpc.moox.ops.SecretMgr", ok.service)
	require.Equal(t, "GetReadonlyExchangeKey", ok.method, "只用专用方法，不碰 GetSecretValue")
	require.Equal(t, "binance", ok.request.GetExchange())

	notFound := &fakeInvoker{response: &adminpb.GetReadonlyExchangeKeyRsp{RetInfo: &adminpb.RetInfo{Code: adminpb.ErrorCode_NOT_FOUND, Msg: "没有配置只读 Key"}}}
	_, err = (&GatewayKeys{Gateway: notFound}).ReadonlyKey(context.Background())
	require.ErrorIs(t, err, ErrReadonlyKeyMissing)

	empty := &fakeInvoker{response: &adminpb.GetReadonlyExchangeKeyRsp{RetInfo: &adminpb.RetInfo{Code: adminpb.ErrorCode_SUCCESS}}}
	_, err = (&GatewayKeys{Gateway: empty}).ReadonlyKey(context.Background())
	require.ErrorIs(t, err, ErrReadonlyKeyMissing)

	// 网关出错、服务内部出错不是“没配置”：来源按失败计数，不会被当成缺 Key 而静默跳过。
	_, err = (&GatewayKeys{Gateway: &fakeInvoker{err: errors.New("网关不可达")}}).ReadonlyKey(context.Background())
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrReadonlyKeyMissing)
	inner := &fakeInvoker{response: &adminpb.GetReadonlyExchangeKeyRsp{RetInfo: &adminpb.RetInfo{Code: adminpb.ErrorCode_INNER_ERR, Msg: "查询失败"}}}
	_, err = (&GatewayKeys{Gateway: inner}).ReadonlyKey(context.Background())
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrReadonlyKeyMissing)
}
