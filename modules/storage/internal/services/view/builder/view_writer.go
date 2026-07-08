package builder

import (
	"context"
	"errors"
	"sync"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

const defaultViewWriterQueueSize = 1024

type viewWriterPool struct {
	sink TimeSeriesViewWriter

	mu      sync.Mutex
	writers map[string]*singleViewWriter
	closed  bool
}

func newViewWriterPool(sink TimeSeriesViewWriter) *viewWriterPool {
	return &viewWriterPool{
		sink:    sink,
		writers: make(map[string]*singleViewWriter),
	}
}

func (p *viewWriterPool) insert(ctx context.Context, tableName string, rows []*pb.TimeSeriesRow) error {
	if p == nil || p.sink == nil {
		return errors.New("view writer is required")
	}
	writer, err := p.writerFor(tableName)
	if err != nil {
		return err
	}
	return writer.insert(ctx, rows)
}

func (p *viewWriterPool) writerFor(tableName string) (*singleViewWriter, error) {
	if tableName == "" {
		return nil, errors.New("view result table name is required")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, errors.New("view writer pool is closed")
	}
	if writer := p.writers[tableName]; writer != nil {
		return writer, nil
	}
	writer := newSingleViewWriter(tableName, p.sink)
	p.writers[tableName] = writer
	return writer, nil
}

func (p *viewWriterPool) close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	writers := make([]*singleViewWriter, 0, len(p.writers))
	for _, writer := range p.writers {
		writers = append(writers, writer)
	}
	p.writers = nil
	p.closed = true
	p.mu.Unlock()
	for _, writer := range writers {
		writer.close()
	}
}

type singleViewWriter struct {
	tableName string
	sink      TimeSeriesViewWriter
	jobs      chan viewWriteRequest
	done      chan struct{}
}

type viewWriteRequest struct {
	ctx  context.Context
	rows []*pb.TimeSeriesRow
	done chan error
}

func newSingleViewWriter(tableName string, sink TimeSeriesViewWriter) *singleViewWriter {
	writer := &singleViewWriter{
		tableName: tableName,
		sink:      sink,
		jobs:      make(chan viewWriteRequest, defaultViewWriterQueueSize),
		done:      make(chan struct{}),
	}
	go writer.run()
	return writer
}

func (w *singleViewWriter) insert(ctx context.Context, rows []*pb.TimeSeriesRow) error {
	if len(rows) == 0 {
		return nil
	}
	req := viewWriteRequest{
		ctx:  ctx,
		rows: rows,
		done: make(chan error, 1),
	}
	select {
	case w.jobs <- req:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-req.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *singleViewWriter) run() {
	defer close(w.done)
	for req := range w.jobs {
		req.done <- w.sink.InsertRows(req.ctx, w.tableName, req.rows)
	}
}

func (w *singleViewWriter) close() {
	close(w.jobs)
	<-w.done
}
