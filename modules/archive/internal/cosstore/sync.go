package cosstore

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/domain"
	"github.com/mooyang-code/moox/modules/archive/internal/journal"
	"github.com/mooyang-code/moox/modules/archive/internal/parquetio"
	"github.com/mooyang-code/moox/modules/archive/internal/partitionlock"
)

type COSRegistry interface {
	RegisterCOS(context.Context, domain.PartitionKey, domain.Manifest, domain.COSState) error
}

type SyncJournal interface {
	PartitionState(context.Context, domain.PartitionKey) (journal.PartitionState, error)
	MarkCOSSynced(context.Context, domain.PartitionKey, domain.COSState) error
}

type Syncer struct {
	Client             ObjectClient
	Journal            SyncJournal
	Registry           COSRegistry
	Root               string
	Prefix             string
	Workers            int
	SyncOpenPartitions bool
	SeriesTag          *string
	PartitionLocks     *partitionlock.Locker
}

func (s Syncer) Sync(ctx context.Context) error {
	if s.Client == nil {
		return fmt.Errorf("cos object client is required")
	}
	if s.Journal == nil {
		return fmt.Errorf("archive journal is required")
	}
	if s.Registry == nil {
		return fmt.Errorf("archive registry is required")
	}
	if s.PartitionLocks == nil {
		return fmt.Errorf("archive partition locker is required")
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
			err := s.syncFile(ctx, path)
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
			partition, err := domain.ParseArchivePath(path)
			if err != nil {
				return err
			}
			if !s.SyncOpenPartitions && partition.Month == time.Now().UTC().Format("200601") {
				return nil
			}
			if s.SeriesTag != nil && partition.SeriesTag != *s.SeriesTag {
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

func (s Syncer) syncFile(ctx context.Context, path string) error {
	partition, err := domain.ParseArchivePath(path)
	if err != nil {
		return err
	}
	unlock := s.PartitionLocks.Lock(domain.PartitionID(partition))
	defer unlock()
	state, err := s.Journal.PartitionState(ctx, partition)
	if err != nil {
		return err
	}
	if state.Manifest == nil || state.Phase != journal.PhaseClean {
		return fmt.Errorf("partition %s has no clean local manifest", domain.PartitionID(partition))
	}
	manifest, err := parquetio.Validate(path, partition, state.Manifest.Generation)
	if err != nil {
		return fmt.Errorf("validate parquet before COS upload: %w", err)
	}
	if manifest.SHA256 != state.Manifest.SHA256 || manifest.Size != state.Manifest.Size ||
		manifest.RowCount != state.Manifest.RowCount {
		return fmt.Errorf("parquet content does not match journal manifest")
	}
	objectKey, err := ObjectKey(s.Root, s.Prefix, path)
	if err != nil {
		return err
	}
	expectedObject := ObjectMetadata{SHA256: manifest.SHA256, Size: manifest.Size}
	if err := s.Client.Put(ctx, objectKey, path, expectedObject); err != nil {
		return err
	}
	actualObject, err := s.Client.Head(ctx, objectKey)
	if err != nil {
		return fmt.Errorf("verify uploaded COS object: %w", err)
	}
	if actualObject != expectedObject {
		return fmt.Errorf("uploaded COS object metadata mismatch")
	}
	cosState := domain.COSState{
		Status:      "synced",
		Generation:  manifest.Generation,
		ObjectKey:   objectKey,
		LastAttempt: time.Now().UTC(),
	}
	if err := s.Registry.RegisterCOS(ctx, partition, manifest, cosState); err != nil {
		return fmt.Errorf("register COS archive generation: %w", err)
	}
	if err := s.Journal.MarkCOSSynced(ctx, partition, cosState); err != nil {
		return fmt.Errorf("persist COS archive generation: %w", err)
	}
	return nil
}
