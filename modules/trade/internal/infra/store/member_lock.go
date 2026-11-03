package store

import "context"

// LockMemberChange 按固定顺序取得成员变更需要的全部锁（组合账户、成员、执行、执行账户）。
// 任何一把等待失败都会释放已取得的锁，返回的函数按相反顺序释放。
func (s *Store) LockMemberChange(ctx context.Context, spaceID, logicalAccountID, tradingAccountID string) (func(), error) {
	var releases []func()
	release := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	for _, lock := range []func() (func(), error){
		func() (func(), error) { return s.LockLogicalAccountContext(ctx, spaceID, logicalAccountID) },
		func() (func(), error) { return s.LockLogicalAccountMembershipContext(ctx) },
		func() (func(), error) { return s.LockLogicalAccountExecutionContext(ctx, spaceID, logicalAccountID) },
		func() (func(), error) { return s.LockTradingAccountContext(ctx, tradingAccountID) },
	} {
		unlock, err := lock()
		if err != nil {
			release()
			return nil, err
		}
		releases = append(releases, unlock)
	}
	return release, nil
}
