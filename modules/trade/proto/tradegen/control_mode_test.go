package tradepb

import "testing"

func TestCreationControlModeValidation(t *testing.T) {
	for _, mode := range []ControlMode{ControlMode_CONTROL_MODE_UNSPECIFIED, ControlMode_CONTROL_MODE_STRATEGY, ControlMode_CONTROL_MODE_MANUAL, ControlMode(-1), ControlMode(99)} {
		t.Run(mode.String(), func(t *testing.T) {
			logical := &CreateLogicalAccountReq{Name: "account", Environment: AccountEnvironment_ACCOUNT_ENVIRONMENT_PAPER, SettlementAsset: "USDT", ControlMode: mode}
			valid := mode >= ControlMode_CONTROL_MODE_UNSPECIFIED && mode <= ControlMode_CONTROL_MODE_MANUAL
			if err := logical.Validate(); (err == nil) != valid {
				t.Errorf("logical: mode %d validation = %v, want valid=%t", mode, err, valid)
			}
		})
	}
}
