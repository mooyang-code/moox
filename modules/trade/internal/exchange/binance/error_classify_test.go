package binance

import (
	"errors"
	"fmt"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// 币安下单错误按错误码和说明文字分类：订单号重复、-1000、-1001 是结果未知，-2010 其余按说明细分，-1016 保留拒单。
func TestClassifyAPIErrorOrderRejections(t *testing.T) {
	for _, test := range []struct {
		name    string
		code    string
		message string
		want    exchange.ErrorKind
	}{
		{"现货订单号重复", "-2010", "Duplicate order sent.", exchange.ErrorTransportUnknown},
		{"现货余额不足", "-2010", "Account has insufficient balance for requested action.", exchange.ErrorInsufficientBalance},
		{"现货市场关闭", "-2010", "Market is closed.", exchange.ErrorRejected},
		{"现货只挂单会立即成交", "-2010", "Order would immediately match and take.", exchange.ErrorRejected},
		{"合约保证金不足", "-2019", "Margin is insufficient.", exchange.ErrorInsufficientBalance},
		{"合约订单号重复", "-4116", "ClientOrderId is duplicated.", exchange.ErrorTransportUnknown},
		{"未知错误", "-1000", "An unknown error occurred while processing the request.", exchange.ErrorTransportUnknown},
		{"内部错误", "-1001", "Internal error; unable to process your request. Please try again.", exchange.ErrorTransportUnknown},
		{"服务关闭中确定未受理", "-1016", "This service is no longer available.", exchange.ErrorRejected},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(`{"code":%s,"msg":%q}`, test.code, test.message))
			err := classifyAPIError(&exchange.Error{Kind: exchange.ErrorRejected, HTTPStatus: 400}, raw)
			if !exchange.IsKind(err, test.want) {
				t.Fatalf("err = %v, want %s", err, test.want)
			}
			var typed *exchange.Error
			if !errors.As(err, &typed) || typed.Code != test.code {
				t.Fatalf("没有保留原错误码 %s：%v", test.code, err)
			}
		})
	}
}
