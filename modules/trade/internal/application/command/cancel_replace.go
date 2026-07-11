package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/ledger"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

func (e *Engine) Cancel(ctx context.Context, space, orderID string) (store.OrderRecord, error) {
	r, err := e.Store.GetOrder(ctx, space, orderID)
	if err != nil {
		return r, err
	}
	o, err := aggregate(r)
	if err != nil {
		return r, err
	}
	expected := o.Version
	if _, err = o.BeginCancel(); err != nil {
		return r, err
	}
	r = recordFrom(r, o)
	if err = e.Store.Transaction(ctx, func(tx *store.Tx) error { return tx.UpdateOrder(r, expected) }); err != nil {
		return r, err
	}
	adapter, resolveErr := e.adapter(ctx, r)
	if resolveErr != nil {
		return r, resolveErr
	}
	_, callErr := adapter.Cancel(ctx, r.Symbol, r.ClientOrderID)
	latest, _ := e.Store.GetOrder(ctx, space, orderID)
	o, _ = aggregate(latest)
	expected = o.Version
	if callErr != nil {
		if exchange.IsCategory(callErr, exchange.ErrorTransportUncertain) {
			_, err = o.MarkCancelUnknown()
		} else {
			_, err = o.CancelFailed()
		}
		if err != nil {
			return latest, err
		}
		latest = recordFrom(latest, o)
		err = e.Store.Transaction(ctx, func(tx *store.Tx) error { return tx.UpdateOrder(latest, expected) })
		if err != nil {
			return latest, err
		}
		return latest, callErr
	}
	_, err = o.ConfirmCancel()
	if err != nil {
		return latest, err
	}
	latest = recordFrom(latest, o)
	err = e.Store.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.UpdateOrder(latest, expected); err != nil {
			return err
		}
		return ReleaseReservation(tx, latest)
	})
	return latest, err
}

func ReleaseReservation(tx *store.Tx, r store.OrderRecord) error {
	reserved, e := shared.ParseDecimal(r.ReservedAmount)
	if e != nil {
		return e
	}
	consumed, e := shared.ParseDecimal(r.ConsumedReserved)
	if e != nil {
		return e
	}
	amount := reserved.Sub(consumed)
	if amount.IsZero() {
		return nil
	}
	if amount.IsNegative() {
		return errors.New("trade: consumed reservation exceeds reserved amount")
	}
	p := ledger.Transaction{ID: shared.LedgerTransactionID("unfreeze:" + r.OrderID), BizType: "unfreeze", RefType: "order", RefID: r.OrderID, Entries: []ledger.Entry{{AccountID: r.AccountID, Asset: r.ReservedAsset, Bucket: "frozen", Amount: amount.Neg()}, {AccountID: r.AccountID, Asset: r.ReservedAsset, Bucket: "available", Amount: amount}}}
	return tx.PostLedger(r.SpaceID, p)
}

func (e *Engine) ResolveCancelUnknown(ctx context.Context, space, orderID string) (store.OrderRecord, error) {
	r, err := e.Store.GetOrder(ctx, space, orderID)
	if err != nil {
		return r, err
	}
	if r.State != string(order.CancelUnknown) {
		return r, nil
	}
	adapter, err := e.adapter(ctx, r)
	if err != nil {
		return r, err
	}
	result, err := adapter.QueryByClientOrderID(ctx, r.Symbol, r.ClientOrderID)
	if err != nil {
		return r, err
	}
	o, err := aggregate(r)
	if err != nil {
		return r, err
	}
	expected := o.Version
	r.ExchangeOrderID = result.ExchangeOrderID
	if result.Status == "CANCELED" {
		// Keep the order recoverable until the fill reconciliation path has
		// ingested every partial fill and then confirms the terminal state.
		if err := e.Store.Transaction(ctx, func(tx *store.Tx) error { return tx.UpdateOrder(r, expected) }); err != nil {
			return r, err
		}
		return r, nil
	} else {
		_, err = o.CancelStillOpen()
	}
	if err != nil {
		return r, err
	}
	r = recordFrom(r, o)
	err = e.Store.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.UpdateOrder(r, expected); err != nil {
			return err
		}
		if result.Status == "CANCELED" {
			return ReleaseReservation(tx, r)
		}
		return nil
	})
	return r, err
}
func (e *Engine) RecoverCanceling(ctx context.Context, space, orderID string) (store.OrderRecord, error) {
	r, err := e.Store.GetOrder(ctx, space, orderID)
	if err != nil {
		return r, err
	}
	if r.State != string(order.Canceling) {
		return r, nil
	}
	o, err := aggregate(r)
	if err != nil {
		return r, err
	}
	expected := o.Version
	if _, err = o.RecoverCanceling(); err != nil {
		return r, err
	}
	r = recordFrom(r, o)
	if err = e.Store.Transaction(ctx, func(tx *store.Tx) error { return tx.UpdateOrder(r, expected) }); err != nil {
		return r, err
	}
	return e.ResolveCancelUnknown(ctx, space, orderID)
}

func (e *Engine) ReconcileExchangeTerminal(ctx context.Context, space, orderID, status string) (store.OrderRecord, error) {
	r, err := e.Store.GetOrder(ctx, space, orderID)
	if err != nil {
		return r, err
	}
	o, err := aggregate(r)
	if err != nil {
		return r, err
	}
	expected := o.Version
	switch status {
	case "CANCELED":
		if o.State != order.Canceling && o.State != order.CancelUnknown {
			if _, err = o.BeginCancel(); err != nil {
				return r, err
			}
		}
		_, err = o.ConfirmCancel()
	case "REJECTED":
		_, err = o.Reject()
	case "EXPIRED":
		_, err = o.Expire()
	default:
		return r, fmt.Errorf("trade: unsupported terminal exchange status %s", status)
	}
	if err != nil {
		return r, err
	}
	r = recordFrom(r, o)
	err = e.Store.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.UpdateOrder(r, expected); err != nil {
			return err
		}
		return ReleaseReservation(tx, r)
	})
	return r, err
}

func (e *Engine) ReconcileExchangeCanceled(ctx context.Context, space, orderID string) (store.OrderRecord, error) {
	return e.ReconcileExchangeTerminal(ctx, space, orderID, "CANCELED")
}

func (e *Engine) Replace(ctx context.Context, sagaID, oldOrderID string, replacement PlaceInput) (store.SagaRecord, error) {
	if sagaID == "" {
		return store.SagaRecord{}, errors.New("trade: saga id required")
	}
	s := execution.NewReplaceSaga(shared.SagaID(sagaID), oldOrderID)
	payload, _ := json.Marshal(replacement)
	rec := store.SagaRecord{SpaceID: replacement.SpaceID, SagaID: sagaID, Type: "CANCEL_REPLACE", State: string(s.State), OrderID: oldOrderID, Payload: string(payload), Version: s.Version}
	if err := e.Store.Transaction(ctx, func(tx *store.Tx) error { return tx.CreateSaga(rec) }); err != nil {
		return rec, err
	}
	old, err := e.Cancel(ctx, replacement.SpaceID, oldOrderID)
	if err != nil {
		if exchange.IsCategory(err, exchange.ErrorTransportUncertain) {
			rec.State = string(execution.SagaCancelUnknown)
		} else {
			rec.State = string(execution.SagaCancelFailed)
		}
		rec.LastError = err.Error()
		rec.Version++
		_ = e.Store.Transaction(ctx, func(tx *store.Tx) error { return tx.UpdateSaga(rec, rec.Version-1) })
		return rec, err
	}
	_ = old
	if err = s.CancelConfirmed(); err != nil {
		return rec, err
	}
	expected := rec.Version
	rec.State = string(s.State)
	rec.Version = s.Version
	if err = e.Store.Transaction(ctx, func(tx *store.Tx) error { return tx.UpdateSaga(rec, expected) }); err != nil {
		return rec, err
	}
	if err = s.ReplacementCreated(replacement.OrderID); err != nil {
		return rec, err
	}
	expected = rec.Version
	rec.State = string(s.State)
	rec.ReplacementOrderID = s.ReplacementOrderID
	rec.Version = s.Version
	if err = e.Store.Transaction(ctx, func(tx *store.Tx) error { return tx.UpdateSaga(rec, expected) }); err != nil {
		return rec, err
	}
	_, err = e.Place(ctx, replacement)
	if err != nil {
		expected = rec.Version
		_ = s.ReplacementResult(false, false, err.Error())
		rec.State = string(s.State)
		rec.Version = s.Version
		rec.LastError = s.LastError
		_ = e.Store.Transaction(ctx, func(tx *store.Tx) error { return tx.UpdateSaga(rec, expected) })
	}
	return rec, err
}

func (e *Engine) AdvanceReplace(ctx context.Context, space, sagaID string) (store.SagaRecord, error) {
	rec, err := e.Store.GetSaga(ctx, space, sagaID)
	if err != nil {
		return rec, err
	}
	if rec.State != string(execution.SagaReplacementCreated) {
		return rec, nil
	}
	var replacement PlaceInput
	if err = json.Unmarshal([]byte(rec.Payload), &replacement); err != nil {
		return rec, err
	}
	r, err := e.Store.GetOrder(ctx, space, rec.ReplacementOrderID)
	if err != nil {
		r, err = e.Place(ctx, replacement)
	}
	if err != nil {
		return rec, err
	}
	if r.State == string(order.Ready) || r.State == string(order.Submitting) {
		return rec, nil
	}
	if r.State == string(order.SubmitUnknown) {
		r, err = e.ResolveUnknown(ctx, space, rec.ReplacementOrderID)
		if err != nil {
			return rec, err
		}
	}
	s := execution.Saga{ID: shared.SagaID(rec.SagaID), OrderID: rec.OrderID, ReplacementOrderID: rec.ReplacementOrderID, State: execution.SagaState(rec.State), Version: rec.Version}
	expected := rec.Version
	if r.State == string(order.SubmitUnknown) {
		_ = s.ReplacementResult(false, true, "")
	} else if r.State == string(order.Open) || r.State == string(order.PartiallyFilled) || r.State == string(order.Filled) {
		_ = s.ReplacementResult(true, false, "")
	} else {
		_ = s.ReplacementResult(false, false, "replacement rejected")
	}
	rec.State = string(s.State)
	rec.Version = s.Version
	rec.LastError = s.LastError
	saveErr := e.Store.Transaction(ctx, func(tx *store.Tx) error { return tx.UpdateSaga(rec, expected) })
	return rec, saveErr
}

func (e *Engine) ResumeReplace(ctx context.Context, space, sagaID string) (store.SagaRecord, error) {
	rec, err := e.Store.GetSaga(ctx, space, sagaID)
	if err != nil {
		return rec, err
	}
	if rec.State == string(execution.SagaReplacementCreated) {
		return e.AdvanceReplace(ctx, space, sagaID)
	}
	if rec.State == string(execution.SagaReplacementSubmitUnknown) {
		r, resolveErr := e.ResolveUnknown(ctx, space, rec.ReplacementOrderID)
		if resolveErr != nil {
			return rec, resolveErr
		}
		s := execution.Saga{ID: shared.SagaID(rec.SagaID), OrderID: rec.OrderID, ReplacementOrderID: rec.ReplacementOrderID, State: execution.SagaReplacementCreated, Version: rec.Version}
		_ = s.ReplacementResult(r.State == string(order.Open), r.State == string(order.SubmitUnknown), "")
		expected := rec.Version
		rec.State = string(s.State)
		rec.Version = s.Version
		err = e.Store.Transaction(ctx, func(tx *store.Tx) error { return tx.UpdateSaga(rec, expected) })
		return rec, err
	}
	if rec.State != string(execution.SagaCancelUnknown) && rec.State != string(execution.SagaCancelRequested) {
		return rec, nil
	}
	old, err := e.Store.GetOrder(ctx, space, rec.OrderID)
	if err != nil {
		return rec, err
	}
	if old.State == string(order.Canceling) {
		old, err = e.RecoverCanceling(ctx, space, rec.OrderID)
	} else if old.State == string(order.CancelUnknown) {
		old, err = e.ResolveCancelUnknown(ctx, space, rec.OrderID)
	} else if rec.State == string(execution.SagaCancelRequested) && old.State != string(order.Canceled) && old.State != string(order.PartiallyCanceled) {
		old, err = e.Cancel(ctx, space, rec.OrderID)
		if err != nil && !exchange.IsCategory(err, exchange.ErrorTransportUncertain) {
			return rec, err
		}
	}
	if err != nil {
		return rec, err
	}
	if old.State != string(order.Canceled) && old.State != string(order.PartiallyCanceled) {
		return rec, nil
	}
	var replacement PlaceInput
	if err = json.Unmarshal([]byte(rec.Payload), &replacement); err != nil {
		return rec, err
	}
	s := execution.Saga{ID: shared.SagaID(rec.SagaID), OrderID: rec.OrderID, State: execution.SagaState(rec.State), Version: rec.Version}
	if err = s.CancelConfirmed(); err != nil {
		return rec, err
	}
	expected := rec.Version
	rec.State = string(s.State)
	rec.Version = s.Version
	if err = e.Store.Transaction(ctx, func(tx *store.Tx) error { return tx.UpdateSaga(rec, expected) }); err != nil {
		return rec, err
	}
	if err = s.ReplacementCreated(replacement.OrderID); err != nil {
		return rec, err
	}
	expected = rec.Version
	rec.State = string(s.State)
	rec.Version = s.Version
	rec.ReplacementOrderID = replacement.OrderID
	if err = e.Store.Transaction(ctx, func(tx *store.Tx) error { return tx.UpdateSaga(rec, expected) }); err != nil {
		return rec, err
	}
	if _, err = e.Place(ctx, replacement); err != nil {
		return rec, err
	}
	return rec, nil
}
