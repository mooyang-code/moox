package operator

import (
	"errors"
	"fmt"
	"strings"
)

var ErrInvalidAction = errors.New("人工操作记录不合法")

type ActionType string

const (
	// ActionManualOrder 是人工下单，按组合的控制模式决定要不要先接管（见 operator.Service.PlaceOrder）。
	ActionManualOrder ActionType = "MANUAL_ORDER"
	ActionCancelOrder ActionType = "CANCEL_ORDER"
	ActionFlatten     ActionType = "FLATTEN"
	// ActionReduce 按比例减仓，清仓就是比例为 100% 的减仓。
	ActionReduce ActionType = "REDUCE"
	// ActionDelistFlatten 对单个下架标的清仓，不暂停组合。
	ActionDelistFlatten ActionType = "DELIST_FLATTEN"
)

type Status string

const (
	StatusRunning   Status = "RUNNING"
	StatusCompleted Status = "COMPLETED"
	StatusPartial   Status = "PARTIAL"
	StatusFailed    Status = "FAILED"
)

type Action struct {
	SpaceID          string
	ID               string
	LogicalAccountID string
	Type             ActionType
	Reason           string
	RequestJSON      string
	Status           Status
	ResultJSON       *string
	LastError        string
}

func (a Action) Validate() error {
	if blank(a.SpaceID) || blank(a.ID) || blank(a.LogicalAccountID) ||
		blank(a.Reason) || blank(a.RequestJSON) ||
		!validType(a.Type) || !validStatus(a.Status) {
		return fmt.Errorf("%w: 缺少必填字段或取值不受支持", ErrInvalidAction)
	}
	return nil
}

func validType(value ActionType) bool {
	return value == ActionManualOrder ||
		value == ActionCancelOrder ||
		value == ActionFlatten ||
		value == ActionReduce ||
		value == ActionDelistFlatten
}

func validStatus(value Status) bool {
	switch value {
	case StatusRunning, StatusCompleted, StatusPartial, StatusFailed:
		return true
	default:
		return false
	}
}

func blank(value string) bool {
	return strings.TrimSpace(value) == ""
}
