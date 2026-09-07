package tradepb

import "testing"

func TestCreationControlModeValidation(t *testing.T) {
	for _, mode := range []ControlMode{ControlMode_CONTROL_MODE_UNSPECIFIED, ControlMode_CONTROL_MODE_STRATEGY, ControlMode_CONTROL_MODE_MANUAL, ControlMode(-1), ControlMode(99)} {
		t.Run(mode.String(), func(t *testing.T) {
			logical := &CreateLogicalAccountReq{Name: "account", ExecutionMode: ExecutionMode_EXECUTION_MODE_PAPER, MarketType: MarketType_MARKET_TYPE_SPOT, SettlementAsset: "USDT", ControlMode: mode}
			paper := &CreatePaperSimulationReq{AccountName: "paper", LogicalAccountName: "logical", Exchange: Exchange_EXCHANGE_BINANCE, MarketType: MarketType_MARKET_TYPE_SPOT, SettlementAsset: "USDT", InitialBalance: "1000", MakerFeeRate: "0", TakerFeeRate: "0", SlippageBps: "0", ControlMode: mode}
			valid := mode >= ControlMode_CONTROL_MODE_UNSPECIFIED && mode <= ControlMode_CONTROL_MODE_MANUAL
			for name, err := range map[string]error{"logical": logical.Validate(), "paper": paper.Validate()} {
				if (err == nil) != valid {
					t.Errorf("%s: mode %d validation = %v, want valid=%t", name, mode, err, valid)
				}
			}
		})
	}
}
