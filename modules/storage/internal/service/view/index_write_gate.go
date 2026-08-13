package view

import "context"

type indexWriteGate struct {
	token chan struct{}
}

func newIndexWriteGate() *indexWriteGate {
	g := &indexWriteGate{token: make(chan struct{}, 1)}
	g.token <- struct{}{}
	return g
}

func (g *indexWriteGate) lock(ctx context.Context) (func(), error) {
	if g == nil {
		return func() {}, nil
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-g.token:
		return func() { g.token <- struct{}{} }, nil
	}
}
