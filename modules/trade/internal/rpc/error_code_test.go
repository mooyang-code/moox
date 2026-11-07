package rpc

import (
	"fmt"
	"testing"

	logicalapp "github.com/mooyang-code/moox/modules/trade/internal/application/logicalaccount"
	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	"github.com/mooyang-code/moox/modules/trade/internal/runtime"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
)

// 会话未启动、需要接管各有专门的错误码，前端按码判断（重试、提示勾选接管），不再匹配消息文字。
func TestErrorInfoDedicatedTradeCodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want tradepb.ErrorCode
	}{
		{"会话未启动", runtime.ErrSessionConfig, tradepb.ErrorCode_TRADE_SESSION_NOT_READY},
		{"包装后的会话未启动", fmt.Errorf("加入组合: %w", runtime.ErrSessionConfig), tradepb.ErrorCode_TRADE_SESSION_NOT_READY},
		{"取执行通道时会话未启动", &orderapp.AccountExecutionError{TradingAccountID: "a", Operation: "adapter", Err: runtime.ErrSessionConfig}, tradepb.ErrorCode_TRADE_SESSION_NOT_READY},
		{"需要接管", logicalapp.ErrAdoptionRequired, tradepb.ErrorCode_TRADE_ADOPTION_REQUIRED},
		{"有持仓不能停用", logicalapp.ErrMemberHasExposure, tradepb.ErrorCode_INVALID_PARAM},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := errorInfo(tc.err)
			if info.GetCode() != tc.want {
				t.Fatalf("code = %v, want %v", info.GetCode(), tc.want)
			}
			if info.GetMsg() != tc.err.Error() {
				t.Fatalf("msg = %q", info.GetMsg())
			}
		})
	}
	if tradepb.ErrorCode_TRADE_SESSION_NOT_READY != 19 || tradepb.ErrorCode_TRADE_ADOPTION_REQUIRED != 20 {
		t.Fatal("前端按数字 19、20 判断，编号不能变")
	}
}
