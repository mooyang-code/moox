package writer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/domain"
	"github.com/mooyang-code/moox/modules/archive/internal/journal"
	"github.com/mooyang-code/moox/modules/archive/internal/parquetio"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type Registry interface {
	Register(context.Context, domain.PartitionKey, domain.Manifest) error
}

type Writer struct {
	journal      *journal.Store
	root         string
	rowGroupRows int64
	workers      int
	locks        sync.Map
	registry     Registry
}

func (w *Writer) SetRegistry(registry Registry) { w.registry = registry }

func New(store *journal.Store, root string, rowGroupRows int64) *Writer {
	if rowGroupRows <= 0 {
		rowGroupRows = 65536
	}
	return &Writer{journal: store, root: root, rowGroupRows: rowGroupRows, workers: 1}
}

func (w *Writer) SetWorkers(workers int) {
	if workers > 0 {
		w.workers = workers
	}
}

func (w *Writer) WritePartition(ctx context.Context, key domain.PartitionKey) (domain.Manifest, error) {
	lock := w.partitionLock(domain.PartitionID(key))
	lock.Lock()
	defer lock.Unlock()
	attempt, err := w.journal.BeginMaterialization(ctx, key)
	if err != nil {
		return domain.Manifest{}, err
	}
	basePath, err := key.AbsolutePath(w.root)
	if err != nil {
		return domain.Manifest{}, err
	}
	rows := map[string]domain.ArchiveRow{}
	schema := map[string]storagepb.FieldValueType{}
	if _, err := os.Stat(basePath); err == nil {
		existing, existingSchema, _, readErr := parquetio.Read(basePath)
		if readErr != nil {
			return domain.Manifest{}, readErr
		}
		for _, row := range existing {
			rows[domain.LogicalRowID(row.DataTime, row.DimensionsJSON)] = row
		}
		for name, kind := range existingSchema {
			schema[name] = kind
		}
	}
	pending, err := w.journal.Pending(ctx, key, attempt.ThroughSeq)
	if err != nil {
		return domain.Manifest{}, err
	}
	sort.SliceStable(pending, func(i, j int) bool { return pending[i].Seq < pending[j].Seq })
	for _, item := range pending {
		row, ok := rows[item.RowID]
		if !ok {
			row = domain.ArchiveRow{Partition: key, DataTime: item.Patch.DataTime, DimensionsJSON: item.Patch.DimensionsJSON, Attributes: map[string]string{}, Columns: map[string]domain.Scalar{}}
		}
		row = domain.MergePatch(row, item.Patch)
		rows[item.RowID] = row
		for name, value := range item.Patch.Columns {
			if old, ok := schema[name]; ok && old != value.Type {
				return domain.Manifest{}, fmt.Errorf("schema conflict for %s", name)
			}
			schema[name] = value.Type
		}
	}
	ordered := make([]domain.ArchiveRow, 0, len(rows))
	for _, row := range rows {
		ordered = append(ordered, row)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].DataTime.Equal(ordered[j].DataTime) {
			return ordered[i].DimensionsJSON < ordered[j].DimensionsJSON
		}
		return ordered[i].DataTime.Before(ordered[j].DataTime)
	})
	if len(ordered) == 0 {
		return domain.Manifest{}, fmt.Errorf("partition has no rows")
	}
	name, err := key.FileName()
	if err != nil {
		return domain.Manifest{}, err
	}
	dir := filepath.Dir(basePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return domain.Manifest{}, err
	}
	tmp := filepath.Join(dir, "."+name+fmt.Sprintf(".tmp-%d", attempt.Generation))
	_ = os.Remove(tmp)
	manifest, err := parquetio.Write(tmp, ordered, parquetio.WriteOptions{Generation: attempt.Generation, MaterializedAt: time.Now().UTC(), RowGroupRows: w.rowGroupRows, Columns: schema})
	if err != nil {
		return domain.Manifest{}, err
	}
	if _, err := parquetio.Validate(tmp, key, attempt.Generation); err != nil {
		return domain.Manifest{}, err
	}
	if err := syncFile(tmp); err != nil {
		return domain.Manifest{}, err
	}
	if err := os.Rename(tmp, basePath); err != nil {
		return domain.Manifest{}, err
	}
	if err := syncDir(dir); err != nil {
		return domain.Manifest{}, err
	}
	manifest.Path = basePath
	if err := w.journal.MarkLocalCommitted(ctx, key, manifest); err != nil {
		return domain.Manifest{}, err
	}
	if w.registry != nil {
		if err := w.registry.Register(ctx, key, manifest); err != nil {
			return domain.Manifest{}, err
		}
	}
	if err := w.journal.MarkRegistered(ctx, key); err != nil {
		return domain.Manifest{}, err
	}
	if err := w.journal.Complete(ctx, key, attempt.ThroughSeq); err != nil {
		return domain.Manifest{}, err
	}
	return manifest, nil
}

func (w *Writer) WriteDirty(ctx context.Context, limit int) error {
	states, err := w.journal.DirtyPartitions(ctx, limit)
	if err != nil {
		return err
	}
	if len(states) == 0 {
		return nil
	}
	workers := w.workers
	jobs := make(chan domain.PartitionKey)
	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for key := range jobs {
				if _, err := w.WritePartition(ctx, key); err != nil {
					select {
					case errCh <- err:
					default:
					}
					return
				}
			}
		}()
	}
	for _, state := range states {
		select {
		case jobs <- state.Key:
		case <-ctx.Done():
			break
		}
	}
	close(jobs)
	wg.Wait()
	select {
	case err := <-errCh:
		return err
	default:
		return ctx.Err()
	}
}

func (w *Writer) PruneMessageReceipts(ctx context.Context, before time.Time) (uint64, error) {
	return w.journal.PruneMessageReceipts(ctx, before)
}
func (w *Writer) Recover(ctx context.Context) error {
	states, err := w.journal.IncompleteMaterializations(ctx)
	if err != nil {
		return err
	}
	for _, state := range states {
		if _, err := w.WritePartition(ctx, state.Key); err != nil {
			return err
		}
	}
	return w.removeUnownedTempFiles(ctx)
}
func (w *Writer) removeUnownedTempFiles(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := os.Stat(w.root); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	return filepath.WalkDir(w.root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || !isTempFile(entry.Name()) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.ModTime().Before(cutoff) {
			return os.Remove(path)
		}
		return nil
	})
}

func isTempFile(name string) bool {
	return len(name) > 5 && name[0] == '.' && containsTempMarker(name)
}

func containsTempMarker(name string) bool {
	for i := 0; i+5 <= len(name); i++ {
		if name[i:i+5] == ".tmp-" {
			return true
		}
	}
	return false
}
func (w *Writer) partitionLock(id string) *sync.Mutex {
	value, _ := w.locks.LoadOrStore(id, &sync.Mutex{})
	return value.(*sync.Mutex)
}
func syncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
