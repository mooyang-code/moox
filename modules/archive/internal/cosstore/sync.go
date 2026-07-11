package cosstore

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Syncer struct {
	Client             ObjectClient
	Root               string
	Prefix             string
	Workers            int
	SyncOpenPartitions bool
}

func (s Syncer) Sync(ctx context.Context) error {
	if s.Client == nil {
		return fmt.Errorf("cos object client is required")
	}
	workers := s.Workers
	if workers <= 0 {
		workers = 2
	}
	paths := make(chan string)
	errCh := make(chan error, 1)
	walkErrCh := make(chan error, 1)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	worker := func() {
		for path := range paths {
			key, err := ObjectKey(s.Root, s.Prefix, path)
			if err == nil {
				err = s.Client.Put(ctx, key, path)
			}
			if err != nil {
				select {
				case errCh <- err:
				default:
				}
				cancel()
				return
			}
		}
	}
	var workersWG sync.WaitGroup
	done := make(chan struct{})
	for i := 0; i < workers; i++ {
		workersWG.Add(1)
		go func() { defer workersWG.Done(); worker() }()
	}
	go func() {
		defer close(done)
		walkErr := filepath.WalkDir(s.Root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".parquet") {
				return nil
			}
			if !s.SyncOpenPartitions && isCurrentMonth(entry.Name()) {
				return nil
			}
			select {
			case paths <- path:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		if walkErr != nil && walkErr != context.Canceled {
			select {
			case walkErrCh <- walkErr:
			default:
			}
		}
		close(paths)
	}()
	<-done
	workersWG.Wait()
	select {
	case err := <-errCh:
		return err
	default:
	}
	select {
	case err := <-walkErrCh:
		return err
	default:
	}
	if err := ctx.Err(); err != nil && err != context.Canceled {
		return err
	}
	return nil
}

func isCurrentMonth(name string) bool {
	if len(name) < len("202601.parquet") {
		return false
	}
	month := name[len(name)-len("202601.parquet") : len(name)-len(".parquet")]
	return month == time.Now().UTC().Format("200601")
}
