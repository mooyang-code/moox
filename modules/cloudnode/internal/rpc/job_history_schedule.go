package rpc

import (
	"context"
	"errors"
	"sync"
	"time"

	"trpc.group/trpc-go/trpc-go/log"
)

type jobHistoryMaintainer interface {
	MaintainDaily(context.Context, time.Time) error
}

const defaultJobHistoryScheduleLocation = "Asia/Shanghai"

var defaultJobHistoryMaintenance = struct {
	sync.RWMutex
	maintainer jobHistoryMaintainer
	now        func() time.Time
}{
	now: time.Now,
}

func SetDefaultJobHistoryMaintainer(maintainer jobHistoryMaintainer) {
	defaultJobHistoryMaintenance.Lock()
	defer defaultJobHistoryMaintenance.Unlock()
	defaultJobHistoryMaintenance.maintainer = maintainer
}

func HandleJobHistorySchedule(ctx context.Context, rawParams string) error {
	defaultJobHistoryMaintenance.RLock()
	maintainer := defaultJobHistoryMaintenance.maintainer
	now := defaultJobHistoryMaintenance.now
	defaultJobHistoryMaintenance.RUnlock()
	if maintainer == nil {
		return errors.New("job history maintainer is not configured")
	}
	if now == nil {
		now = time.Now
	}
	runAt := now().In(jobHistoryScheduleLocation())
	log.InfoContextf(ctx, "cloudnode job history schedule triggered params=%s day=%s", rawParams, runAt.Format("20060102"))
	return maintainer.MaintainDaily(ctx, runAt)
}

func jobHistoryScheduleLocation() *time.Location {
	loc, err := time.LoadLocation(defaultJobHistoryScheduleLocation)
	if err != nil {
		return time.Local
	}
	return loc
}
