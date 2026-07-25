package bootstrap

type kernelWakeup struct {
	ch chan struct{}
}

func newKernelWakeup() *kernelWakeup {
	return &kernelWakeup{ch: make(chan struct{}, 1)}
}

func (w *kernelWakeup) Wake() {
	if w == nil {
		return
	}
	select {
	case w.ch <- struct{}{}:
	default:
	}
}

func (w *kernelWakeup) C() <-chan struct{} {
	if w == nil {
		return nil
	}
	return w.ch
}
