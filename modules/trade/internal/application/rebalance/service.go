package rebalance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	domain "github.com/mooyang-code/moox/modules/trade/internal/domain/rebalance"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
)

type Market struct{ MarketType, BaseAsset, QuoteAsset, Price string }
type CreateInput struct {
	SpaceID, RunID, IdempotencyKey, AccountID, ChannelID, MarketSnapshotID, PositionSnapshotID, RulesVersion string
	Mode                                                                                                     domain.TargetMode
	Targets                                                                                                  []domain.Target
	Currents                                                                                                 []domain.Current
	Markets                                                                                                  map[string]Market
}
type Service struct {
	Store  *store.Store
	Engine *command.Engine
}

func (s Service) Create(ctx context.Context, in CreateInput) error {
	if in.RunID == "" || in.IdempotencyKey == "" || in.MarketSnapshotID == "" || in.PositionSnapshotID == "" {
		return errors.New("trade: incomplete rebalance snapshots")
	}
	legs, err := (domain.Planner{}).BuildMode(in.Mode, in.Targets, in.Currents)
	if err != nil {
		return err
	}
	records := make([]store.RebalanceLegRecord, len(legs))
	for i, l := range legs {
		m, ok := in.Markets[l.Symbol]
		if !ok || m.Price == "" {
			return fmt.Errorf("trade: market snapshot missing %s", l.Symbol)
		}
		records[i] = store.RebalanceLegRecord{SpaceID: in.SpaceID, RunID: in.RunID, LegID: fmt.Sprintf("%s-%d", in.RunID, l.Sequence), Symbol: l.Symbol, MarketType: m.MarketType, BaseAsset: m.BaseAsset, QuoteAsset: m.QuoteAsset, Side: l.Side, Action: string(l.Action), Quantity: l.Quantity.String(), Price: m.Price, ReduceOnly: l.ReduceOnly, Sequence: l.Sequence, DependsOn: l.DependsOn, Status: "PLANNED"}
	}
	run := store.RebalanceRunRecord{SpaceID: in.SpaceID, RunID: in.RunID, AccountID: in.AccountID, ChannelID: in.ChannelID, IdempotencyKey: in.IdempotencyKey, MarketSnapshotID: in.MarketSnapshotID, PositionSnapshotID: in.PositionSnapshotID, RulesVersion: in.RulesVersion, AlgorithmName: "target_position", AlgorithmVersion: "1", Status: "PLANNED", Residual: "{}", Version: 1}
	return s.Store.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.CreateRebalance(run, records); err != nil {
			return err
		}
		b, _ := json.Marshal(run)
		return tx.AddOutbox(in.RunID+":requested", "moox.trade.rebalance.requested.v1", b)
	})
}
func (s Service) Advance(ctx context.Context, space, runID, accountID, channelID string) (string, error) {
	legs, err := s.Store.ListRebalanceLegs(ctx, space, runID)
	if err != nil {
		return "", err
	}
	completed := map[int]bool{}
	failed := false
	for i := range legs {
		l := &legs[i]
		if l.PlanID != "" {
			o, e := s.Store.GetOrder(ctx, space, l.PlanID)
			if e != nil {
				return "", e
			}
			switch order.State(o.State) {
			case order.Filled:
				l.Status = "COMPLETED"
				completed[l.Sequence] = true
				_ = s.Store.Transaction(ctx, func(tx *store.Tx) error { return tx.UpdateRebalanceLeg(space, l.LegID, l.Status, l.PlanID) })
			case order.Rejected, order.Canceled, order.PartiallyCanceled, order.Expired:
				l.Status = "FAILED"
				failed = true
				_ = s.Store.Transaction(ctx, func(tx *store.Tx) error { return tx.UpdateRebalanceLeg(space, l.LegID, l.Status, l.PlanID) })
			}
		}
	}
	if failed {
		telemetry.Rebalances.WithLabelValues("failed").Inc()
		_ = s.Store.Transaction(ctx, func(tx *store.Tx) error { return tx.UpdateRebalanceRun(space, runID, "FAILED", "{}") })
		return "FAILED", nil
	}
	for i := range legs {
		l := &legs[i]
		if l.Status != "PLANNED" {
			continue
		}
		ready := true
		for _, d := range l.DependsOn {
			if !completed[d] {
				ready = false
				break
			}
		}
		if !ready {
			continue
		}
		orderID := l.LegID + "-order"
		_, e := s.Engine.Place(ctx, command.PlaceInput{SpaceID: space, OrderID: orderID, ClientOrderID: orderID, AccountID: accountID, ChannelID: channelID, Symbol: l.Symbol, MarketType: l.MarketType, BaseAsset: l.BaseAsset, QuoteAsset: l.QuoteAsset, Side: l.Side, Quantity: l.Quantity, Price: l.Price, ReduceOnly: l.ReduceOnly})
		if e != nil {
			return "", e
		}
		l.PlanID = orderID
		l.Status = "SUBMITTED"
		if e = s.Store.Transaction(ctx, func(tx *store.Tx) error { return tx.UpdateRebalanceLeg(space, l.LegID, l.Status, l.PlanID) }); e != nil {
			return "", e
		}
	}
	all := len(legs) > 0
	for _, l := range legs {
		if l.Status != "COMPLETED" {
			all = false
		}
	}
	status := "EXECUTING"
	if all {
		status = "COMPLETED"
		telemetry.Rebalances.WithLabelValues("completed").Inc()
	}
	_ = s.Store.Transaction(ctx, func(tx *store.Tx) error {
		residual := "{}"
		if status == "COMPLETED" {
			residual = `{"remaining":"0"}`
		}
		if err := tx.UpdateRebalanceRun(space, runID, status, residual); err != nil {
			return err
		}
		if status == "COMPLETED" {
			return tx.AddOutbox(runID+":completed", "moox.trade.rebalance.completed.v1", []byte(runID))
		}
		return nil
	})
	return status, nil
}
