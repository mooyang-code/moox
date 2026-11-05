package delist

import (
	"context"
	"errors"
	"fmt"
	"strings"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
)

// GatewayKeys 经网关向 SecretMgr 取币安只读 Key 的公开标识。
// Collector 只被放行 GetReadonlyExchangeKey 这一个方法：拿不到任何密钥明文，也拿不到 Trade 的交易密钥。
type GatewayKeys struct {
	Gateway gatewayclient.Invoker
}

// ReadonlyKey 实现 KeyProvider。读不到（没配置、停用、已删除）返回 ErrReadonlyKeyMissing。
func (g *GatewayKeys) ReadonlyKey(ctx context.Context) (string, error) {
	var response adminpb.GetReadonlyExchangeKeyRsp
	if err := g.Gateway.Invoke(ctx, "trpc.moox.ops.SecretMgr", "GetReadonlyExchangeKey", &adminpb.GetReadonlyExchangeKeyReq{Exchange: "binance"}, &response); err != nil {
		return "", fmt.Errorf("读取只读 Key 失败: %w", err)
	}
	switch response.GetRetInfo().GetCode() {
	case adminpb.ErrorCode_SUCCESS:
		if strings.TrimSpace(response.GetKeyId()) == "" {
			return "", ErrReadonlyKeyMissing
		}
		return response.GetKeyId(), nil
	case adminpb.ErrorCode_NOT_FOUND:
		return "", ErrReadonlyKeyMissing
	}
	return "", errors.New("读取只读 Key 失败: " + response.GetRetInfo().GetMsg())
}
