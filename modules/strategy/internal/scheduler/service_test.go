package scheduler

import (
	"context"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"testing"
)

func TestServiceRejectsEnqueueAfterStop(t *testing.T) {
	s := New(nil, 1)
	s.Start(context.Background())
	s.Stop()
	if err := s.Enqueue(context.Background(), domain.Task{BindingID: "b"}); err == nil {
		t.Fatal("expected stopped scheduler error")
	}
}
