package execution

import (
	"context"
	"testing"
)

func TestPaperIsIdempotent(t *testing.T) {
	p := NewPaper()
	r := Request{ExecutionID: "e", IdempotencyKey: "k"}
	a, _ := p.Submit(context.Background(), r)
	b, _ := p.Submit(context.Background(), r)
	if a.ExecutionID != b.ExecutionID {
		t.Fatal()
	}
}
