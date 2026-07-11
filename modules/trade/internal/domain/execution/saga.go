package execution

import (
	"errors"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

var ErrInvalidSagaTransition = errors.New("trade: invalid saga transition")

type SagaState string

const (
	SagaCancelRequested          SagaState = "CANCEL_REQUESTED"
	SagaCancelConfirmed          SagaState = "CANCEL_CONFIRMED"
	SagaReplacementCreated       SagaState = "REPLACEMENT_CREATED"
	SagaReplacementSubmitted     SagaState = "REPLACEMENT_SUBMITTED"
	SagaReplaceFailedAfterCancel SagaState = "REPLACE_FAILED_AFTER_CANCEL"
	SagaCancelUnknown            SagaState = "CANCEL_UNKNOWN"
	SagaReplacementSubmitUnknown SagaState = "REPLACEMENT_SUBMIT_UNKNOWN"
)

type Saga struct {
	ID                          shared.SagaID
	OrderID, ReplacementOrderID string
	State                       SagaState
	Version                     uint64
	LastError                   string
}

func NewReplaceSaga(id shared.SagaID, orderID string) Saga {
	return Saga{ID: id, OrderID: orderID, State: SagaCancelRequested, Version: 1}
}
func (s *Saga) CancelConfirmed() error {
	if s.State != SagaCancelRequested && s.State != SagaCancelUnknown {
		return ErrInvalidSagaTransition
	}
	s.State = SagaCancelConfirmed
	s.Version++
	return nil
}
func (s *Saga) ReplacementCreated(id string) error {
	if s.State != SagaCancelConfirmed || id == "" {
		return ErrInvalidSagaTransition
	}
	s.ReplacementOrderID = id
	s.State = SagaReplacementCreated
	s.Version++
	return nil
}
func (s *Saga) ReplacementResult(ack bool, uncertain bool, reason string) error {
	if s.State != SagaReplacementCreated {
		return ErrInvalidSagaTransition
	}
	s.LastError = reason
	if ack {
		s.State = SagaReplacementSubmitted
	} else if uncertain {
		s.State = SagaReplacementSubmitUnknown
	} else {
		s.State = SagaReplaceFailedAfterCancel
	}
	s.Version++
	return nil
}
