package marketfetch

import "context"

func (s *Scheduler) lifetime() context.Context {
	s.workerMu.Lock()
	defer s.workerMu.Unlock()
	return s.lifetimeLocked()
}

func (s *Scheduler) lifetimeLocked() context.Context {
	if s.workerContext == nil {
		parent := s.Lifetime
		if parent == nil {
			parent = context.Background()
		}
		s.workerContext, s.cancelWorkers = context.WithCancel(parent)
		if s.workersClosed {
			s.cancelWorkers()
		}
	}
	return s.workerContext
}

func (s *Scheduler) launch(work func()) bool {
	s.workerMu.Lock()
	defer s.workerMu.Unlock()
	if s.workersClosed || s.lifetimeLocked().Err() != nil {
		return false
	}
	s.workers.Add(1)
	go func() { defer s.workers.Done(); work() }()
	return true
}

// Close cancels dispatch and retry-maintenance work and waits for all jobs.
// The owner first stops timer and maintenance producers.
func (s *Scheduler) Close() error {
	s.workerMu.Lock()
	s.workersClosed = true
	if s.cancelWorkers != nil {
		s.cancelWorkers()
	}
	s.workerMu.Unlock()
	s.workers.Wait()
	return nil
}
