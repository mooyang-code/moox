package operator

import (
	"errors"
	"fmt"
	"strings"
)

var ErrInvalidAction = errors.New("trade: invalid operator action")

type ActionType string

const (
	ActionManualOrder ActionType = "MANUAL_ORDER"
	ActionSubmitOrder ActionType = "SUBMIT_ORDER"
	ActionCancelOrder ActionType = "CANCEL_ORDER"
	ActionFlatten     ActionType = "FLATTEN"
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
		return fmt.Errorf("%w: missing or unsupported required field", ErrInvalidAction)
	}
	return nil
}

func validType(value ActionType) bool {
	return value == ActionManualOrder ||
		value == ActionSubmitOrder ||
		value == ActionCancelOrder ||
		value == ActionFlatten
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
