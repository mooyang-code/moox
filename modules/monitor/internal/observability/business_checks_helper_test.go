package observability

import (
	"context"
	"time"
)

// testOverview 是测试用的概览加本轮业务检查：页面的 Build 不再算业务检查，评估另调 BusinessChecks。
type testOverview struct {
	Overview
	BusinessChecks []BusinessStatus
}

// buildForTest 同时算概览与业务检查；spaceID 不为空时只留这个空间的业务检查。
func (b Builder) buildForTest(ctx context.Context, spaceID string) (testOverview, error) {
	overview, err := b.Build(ctx, spaceID)
	if err != nil {
		return testOverview{}, err
	}
	now := time.Now().UTC()
	if b.Now != nil {
		now = b.Now().UTC()
	}
	items, err := b.BusinessChecks(ctx, now)
	if err != nil {
		return testOverview{}, err
	}
	out := testOverview{Overview: overview}
	for _, item := range items {
		if spaceID == "" || item.SpaceID == spaceID {
			out.BusinessChecks = append(out.BusinessChecks, item)
		}
	}
	return out, nil
}
