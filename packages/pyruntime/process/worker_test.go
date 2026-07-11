package process

import (
	"context"
	"testing"
)

func TestSupervisorRequiresFactory(t *testing.T) {
	s := NewSupervisor(func(context.Context) (Worker, error) { return nil, context.Canceled }, SupervisorConfig{})
	if _, err := s.Ensure(context.Background()); err == nil {
		t.Fatal("expected factory error")
	}
}
