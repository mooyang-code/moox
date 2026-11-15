package storetest

import (
	"errors"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"gorm.io/gorm"
)

// EnsureCurrentTarget 让组合的当前目标行属于 instanceID：已经是它的就不动；没有目标行时写一行空目标
// （目标号 target-current、会话 session-1、开始于 1000 毫秒、有效一小时）。目标单只能由当前目标行所属的实例落库，
// 只想搭一笔目标单、不关心目标内容的测试用它补上前提。组合上已是别的实例的目标时返回冲突，由调用方自己搭。
func EnsureCurrentTarget(tx *store.Tx, spaceID, logicalAccountID, instanceID string) error {
	current, err := tx.GetCurrentTarget(spaceID, logicalAccountID)
	if err == nil && current.InstanceID == instanceID {
		return nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err == nil {
		return store.ErrConflict
	}
	now := time.Now().UTC()
	_, _, err = tx.AcceptLogicalAccountTarget(store.LogicalAccountTargetRecord{
		SpaceID: spaceID, LogicalAccountID: logicalAccountID, TargetID: "target-current",
		InstanceID: instanceID, SessionID: "session-1", SessionStartedAt: 1_000, StrategyID: "strategy-1",
		BarEndTime: now.Add(-time.Second).UnixMilli(), ValidUntil: now.Add(time.Hour).UnixMilli(),
		Targets: []store.InstrumentTarget{}, Status: "PENDING", AcceptedAt: now.UnixMilli(),
	})
	return err
}
