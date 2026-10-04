package subjectsync

import (
	"context"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type runCountTagStore struct {
	listCalls int
}

func (s *runCountTagStore) ListTags(context.Context) ([]*pb.Tag, error) {
	s.listCalls++
	return nil, nil
}

func (*runCountTagStore) ApplyTagSnapshot(context.Context, string, string, time.Time, []*pb.TagSnapshotItem) error {
	return nil
}

func (*runCountTagStore) ReportTagRunFailure(context.Context, string, string, time.Time, string) error {
	return nil
}

func TestServiceRunDoesNotPollCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := &runCountTagStore{}

	(&Service{Tags: &TagRunner{Store: store}, Poll: time.Millisecond}).Run(ctx)

	if store.listCalls != 0 {
		t.Fatalf("list calls = %d, want 0", store.listCalls)
	}
}
