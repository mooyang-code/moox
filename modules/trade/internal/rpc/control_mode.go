package rpc

import tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"

func controlModeFromPB(mode tradepb.ControlMode) string {
	switch mode {
	case tradepb.ControlMode_CONTROL_MODE_UNSPECIFIED, tradepb.ControlMode_CONTROL_MODE_STRATEGY:
		return "STRATEGY"
	case tradepb.ControlMode_CONTROL_MODE_MANUAL:
		return "MANUAL"
	default:
		return ""
	}
}

func controlModeToPB(mode string) tradepb.ControlMode {
	switch mode {
	case "STRATEGY":
		return tradepb.ControlMode_CONTROL_MODE_STRATEGY
	case "MANUAL":
		return tradepb.ControlMode_CONTROL_MODE_MANUAL
	default:
		return tradepb.ControlMode_CONTROL_MODE_UNSPECIFIED
	}
}
