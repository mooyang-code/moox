package delist

// 总审查的回归用例。

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/store"
)

// 中 1：调用 Trade 的暂时失败都放进这次推进的问题说明（工作线程据此累计连续失败次数），包括查平仓结果出错；恢复后说明为空。
func TestTransientTradeErrorsAreReportedInOutcome(t *testing.T) {
	h := newHarness(t)
	delistAt := baseTime.Add(48 * time.Hour)
	h.trade.setHolders(live("crypto", "la-1"))
	h.addExecution("e1", delistAt, "ALL")
	at := flattenAt(h, "e1", delistAt)
	require.Equal(t, store.DelistRunning, h.steps("e1")[flattenKey(RoundFlatten, "crypto", "la-1")].Status)

	h.trade.setErr("GetAction", errors.New("Trade 不可达"))
	at = at.Add(30 * time.Second)
	outcome := h.step("e1", at)
	require.Equal(t, store.StatusPending, outcome.Status)
	require.Contains(t, outcome.Error, "查询平仓结果失败")
	require.Equal(t, at.Add(30*time.Second), outcome.NextRunAt)
	require.Zero(t, h.steps("e1")[flattenKey(RoundFlatten, "crypto", "la-1")].Attempts, "查询出错不计这一行的重试次数")

	h.trade.setErr("GetAction", nil)
	outcome = h.step("e1", at.Add(30*time.Second))
	require.Empty(t, outcome.Error)
	require.Equal(t, store.DelistSucceeded, h.steps("e1")[flattenKey(RoundFlatten, "crypto", "la-1")].Status)

	// 核对时查持有者出错同样带说明。
	h.trade.setErr("ListHolders", errors.New("Trade 不可达"))
	outcome = h.step("e1", delistAt.Add(10*time.Minute))
	require.Equal(t, store.StatusPending, outcome.Status)
	require.Contains(t, outcome.Error, "核对时查持有者失败")
}
