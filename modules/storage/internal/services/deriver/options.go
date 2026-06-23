package deriver

import "time"

// Options controls the storage deriver service.
type Options struct {
	BatchSize  int
	BatchWait  time.Duration
	MaxWorkers int
}

// BatchOptions controls batch aggregation.
type BatchOptions struct {
	BatchSize int
	BatchWait time.Duration
}

func normalizeBatchOptions(opts BatchOptions) BatchOptions {
	if opts.BatchSize <= 0 {
		opts.BatchSize = 500
	}
	if opts.BatchWait <= 0 {
		opts.BatchWait = 200 * time.Millisecond
	}
	return opts
}
