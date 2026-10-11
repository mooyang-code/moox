package subjectsync

import (
	"context"
	"errors"
	"sync"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// Result statuses reported by a manual sync.
const (
	StatusSuccess = "success"
	StatusFailed  = "failed"
	StatusSkipped = "skipped"
)

// ErrBusy means another sync (scheduled or manual) is already running.
var ErrBusy = errors.New("subject sync is already running")

// Filter limits a manual sync. Empty fields select everything.
type Filter struct {
	SpaceID string
	TagID   string
}

func (f Filter) matchTag(tag *pb.Tag) bool {
	return tag != nil && (f.SpaceID == "" || f.SpaceID == tag.GetSpaceId()) && (f.TagID == "" || f.TagID == tag.GetTagId())
}

// TagResult is the outcome for one tag.
type TagResult struct {
	SpaceID  string
	TagID    string
	Status   string
	Subjects int
	Message  string
}

// AttributeResult is the outcome for one attribute job.
type AttributeResult struct {
	SpaceID string
	Status  string
	Message string
}

// Report is the outcome of a manual sync.
type Report struct {
	Tags       []TagResult
	Attributes []AttributeResult
}

// Failed reports whether any selected tag or job failed.
func (r Report) Failed() bool {
	for _, tag := range r.Tags {
		if tag.Status == StatusFailed {
			return true
		}
	}
	for _, attribute := range r.Attributes {
		if attribute.Status == StatusFailed {
			return true
		}
	}
	return false
}

// Syncer serializes the scheduled timers and manual syncs, so two runs never
// write the same tag snapshot at once.
type Syncer struct {
	Tags       *TagRunner
	Attributes *AttributeRunner
	mu         sync.Mutex
}

// RunDueTags is the scheduled entry point: it waits for a running manual sync.
func (s *Syncer) RunDueTags(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Tags.RunOnce(ctx)
}

// RunDueAttributes is the scheduled entry point for attribute jobs.
func (s *Syncer) RunDueAttributes(ctx context.Context) error {
	// 触发时间在等待锁之前固定：等待手动同步期间跨过计划分钟时仍按触发时刻判断到期
	triggeredAt := s.Attributes.Current()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Attributes.RunOnceAt(ctx, triggeredAt)
}

// SyncNow runs the selected tags (and optionally attribute jobs) immediately.
// It refuses with ErrBusy instead of queueing behind a running sync.
func (s *Syncer) SyncNow(ctx context.Context, filter Filter, includeAttributes bool) (Report, error) {
	if !s.mu.TryLock() {
		return Report{}, ErrBusy
	}
	defer s.mu.Unlock()
	var report Report
	tags, err := s.Tags.SyncTags(ctx, filter)
	if err != nil {
		return Report{}, err
	}
	report.Tags = tags
	if includeAttributes {
		report.Attributes = s.Attributes.SyncAttributes(ctx, filter)
	}
	return report, nil
}
