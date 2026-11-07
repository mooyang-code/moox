package wallet

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
)

// ResolveResult 是人工结束划转记录的结果。
type ResolveResult struct {
	// State 是处理后记录的状态。
	State string
	// AlreadySettled 为真表示记录在这次调用之前就已经结束（自动对账或另一次人工处理抢先），没有改动。
	AlreadySettled bool
}

// Resolve 人工结束一条还在 PENDING 的划转记录：用户到交易所核对划转记录后，到账了记 COMPLETED，没到账记 FAILED。
// 币安的划转接口没有客户端流水号，自动对账一直对不出结果（交易所记录里查不到又不能断定没到账）时，这个账户会一直不发起新划转，
// 所以要有一个人工出口。只允许处理超过 pendingAlertAfter（10 分钟）的记录：太新的交给自动对账。
// 人工记 FAILED 带“未到账”软标记，不计入连续失败，允许这个目标重试；记 COMPLETED 的完成时间取当前时间，随后照常等现货钱包快照追上。
func Resolve(ctx context.Context, st *store.Store, spaceID, transferID, result, note string, now time.Time) (ResolveResult, error) {
	if result != store.TransferStateCompleted && result != store.TransferStateFailed {
		return ResolveResult{}, fmt.Errorf("%w：结果只能是 COMPLETED 或 FAILED", store.ErrInvalidRecord)
	}
	row, err := st.GetWalletTransfer(ctx, spaceID, transferID)
	if err != nil {
		return ResolveResult{}, err
	}
	if row.State != store.TransferStatePending {
		return ResolveResult{State: row.State, AlreadySettled: true}, nil
	}
	if age := now.Sub(time.UnixMilli(row.RequestedAt)); age < pendingAlertAfter {
		return ResolveResult{}, fmt.Errorf("%w：这笔划转 %d 分钟前才发出，不到 %d 分钟先等自动对账",
			store.ErrInvalidRecord, int(age/time.Minute), int(pendingAlertAfter/time.Minute))
	}
	state, lastError, completedAt := result, "", int64(0)
	if result == store.TransferStateCompleted {
		completedAt = now.UnixMilli()
	} else {
		lastError = softNotArrive + "人工核对交易所划转记录后确认没到账"
		if note = strings.TrimSpace(note); note != "" {
			lastError += "：" + note
		}
	}
	err = st.Transaction(ctx, func(tx *store.Tx) error {
		return tx.UpdateWalletTransfer(spaceID, transferID, row.ExchangeTransferID, state, lastError, completedAt)
	})
	if errors.Is(err, store.ErrNotPending) {
		// 提交前被自动对账抢先：以最新状态为准。
		latest, getErr := st.GetWalletTransfer(ctx, spaceID, transferID)
		if getErr != nil {
			return ResolveResult{}, getErr
		}
		return ResolveResult{State: latest.State, AlreadySettled: true}, nil
	}
	if err != nil {
		return ResolveResult{}, err
	}
	return ResolveResult{State: state}, nil
}
