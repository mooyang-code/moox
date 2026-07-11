package pool

import (
	"context"
	"github.com/mooyang-code/moox/packages/pyruntime/process"
	"testing"
)

func TestPoolRejectsNilFactoryResult(t *testing.T) {
	p := New(1, func(context.Context) (process.Worker, error) { return nil, context.Canceled })
	_, err := p.Run(context.Background(), Request{Run: process.RunRequest{RequestID: "x"}})
	if err == nil {
		t.Fatal("expected error")
	}
}
