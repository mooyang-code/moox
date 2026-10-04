package cosstore

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/domain"
	"github.com/mooyang-code/moox/modules/archive/internal/journal"
	"github.com/mooyang-code/moox/modules/archive/internal/parquetio"
	"github.com/mooyang-code/moox/modules/archive/internal/partitionlock"
	"github.com/mooyang-code/moox/modules/archive/internal/writer"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeObjectClient struct {
	puts     []string
	metadata map[string]ObjectMetadata
	err      error
	headErr  error
	head     *ObjectMetadata
	onPut    func()
}

func (f *fakeObjectClient) Put(_ context.Context, key, localPath string, metadata ObjectMetadata) error {
	f.puts = append(f.puts, key+":"+localPath)
	if f.metadata == nil {
		f.metadata = map[string]ObjectMetadata{}
	}
	f.metadata[key] = metadata
	if f.onPut != nil {
		f.onPut()
	}
	return f.err
}

func (f *fakeObjectClient) Head(_ context.Context, key string) (ObjectMetadata, error) {
	if f.headErr != nil {
		return ObjectMetadata{}, f.headErr
	}
	if f.head != nil {
		return *f.head, nil
	}
	return f.metadata[key], nil
}

type fakeCOSRegistry struct {
	keys      []domain.PartitionKey
	manifests []domain.Manifest
	states    []domain.COSState
	err       error
}

func (f *fakeCOSRegistry) RegisterCOS(_ context.Context, key domain.PartitionKey, manifest domain.Manifest, state domain.COSState) error {
	if f.err != nil {
		return f.err
	}
	f.keys = append(f.keys, key)
	f.manifests = append(f.manifests, manifest)
	f.states = append(f.states, state)
	return nil
}

type generationRegistry struct {
	mu          sync.Mutex
	generations []uint64
}

func (r *generationRegistry) record(generation uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.generations) > 0 && generation < r.generations[len(r.generations)-1] {
		return assert.AnError
	}
	r.generations = append(r.generations, generation)
	return nil
}

func (r *generationRegistry) Register(_ context.Context, _ domain.PartitionKey, manifest domain.Manifest) error {
	return r.record(manifest.Generation)
}

func (r *generationRegistry) RegisterCOS(_ context.Context, _ domain.PartitionKey, manifest domain.Manifest, _ domain.COSState) error {
	return r.record(manifest.Generation)
}

func materializePartition(t *testing.T, store *journal.Store, root, tag, month string) (domain.PartitionKey, string) {
	t.Helper()
	key := domain.PartitionKey{SpaceID: "crypto", DatasetID: "kline", SubjectID: "BTC", Freq: "1h", SeriesTag: tag, Month: month}
	closeValue := 1.25
	dataTime, err := time.Parse("200601", month)
	require.NoError(t, err)
	_, err = store.Append(t.Context(), domain.EventBatch{
		MessageID: "event-" + tag + "-" + month,
		Rows: []domain.RowPatch{{
			Partition: key, DataTime: dataTime.UTC(), WrittenAt: time.Now().UTC(),
			Columns: map[string]domain.Scalar{"close": {Type: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Double: &closeValue}},
		}},
	})
	require.NoError(t, err)
	_, err = writer.New(store, root, 1024).WritePartition(t.Context(), key)
	require.NoError(t, err)
	path, err := key.AbsolutePath(root)
	require.NoError(t, err)
	return key, path
}

func newSyncFixture(t *testing.T) (string, *journal.Store, *fakeObjectClient, *fakeCOSRegistry) {
	t.Helper()
	root := t.TempDir()
	store, err := journal.Open(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return root, store, &fakeObjectClient{}, &fakeCOSRegistry{}
}

func TestSyncValidatesUploadsAndClosesGenerationInJournalAndRegistry(t *testing.T) {
	root, store, client, registry := newSyncFixture(t)
	pastMonth := time.Now().UTC().AddDate(0, -2, 0).Format("200601")
	key, _ := materializePartition(t, store, root, "venue:binance", pastMonth)

	err := Syncer{Client: client, Journal: store, Registry: registry, Root: root, Prefix: "moox/archive", Workers: 1, PartitionLocks: partitionlock.New()}.Sync(t.Context())
	require.NoError(t, err)
	require.Len(t, client.puts, 1)
	require.Len(t, registry.states, 1)
	state, err := store.PartitionState(t.Context(), key)
	require.NoError(t, err)
	assert.Equal(t, "synced", state.COS.Status)
	assert.Equal(t, state.Manifest.Generation, state.COS.Generation)
	assert.Equal(t, state.Manifest.Generation, registry.manifests[0].Generation)
	assert.Equal(t, state.Manifest.SHA256, registry.manifests[0].SHA256)
	assert.Contains(t, state.COS.ObjectKey, "moox/archive/")
	assert.Equal(t, ObjectMetadata{SHA256: state.Manifest.SHA256, Size: state.Manifest.Size}, client.metadata[state.COS.ObjectKey])
}

func TestSyncRejectsOrdinaryBytesBeforeUpload(t *testing.T) {
	root, store, client, registry := newSyncFixture(t)
	pastMonth := time.Now().UTC().AddDate(0, -2, 0).Format("200601")
	_, path := materializePartition(t, store, root, "venue:okx", pastMonth)
	require.NoError(t, os.WriteFile(path, []byte("not parquet"), 0o600))

	err := Syncer{Client: client, Journal: store, Registry: registry, Root: root, PartitionLocks: partitionlock.New()}.Sync(t.Context())
	require.ErrorContains(t, err, "validate parquet")
	assert.Empty(t, client.puts)
	assert.Empty(t, registry.states)
}

func TestSyncRejectsValidParquetWhoseHashDiffersFromJournalManifest(t *testing.T) {
	root, store, client, registry := newSyncFixture(t)
	pastMonth := time.Now().UTC().AddDate(0, -2, 0).Format("200601")
	key, path := materializePartition(t, store, root, "venue:okx", pastMonth)
	state, err := store.PartitionState(t.Context(), key)
	require.NoError(t, err)
	rows, columns, _, err := parquetio.Read(path)
	require.NoError(t, err)
	revised := 999.0
	rows[0].Columns["close"] = domain.Scalar{Type: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Double: &revised}
	_, err = parquetio.Write(path, rows, parquetio.WriteOptions{
		Generation: state.Manifest.Generation, MaterializedAt: time.Now().UTC(), RowGroupRows: 1024, Columns: columns,
	})
	require.NoError(t, err)

	err = Syncer{Client: client, Journal: store, Registry: registry, Root: root, PartitionLocks: partitionlock.New()}.Sync(t.Context())
	require.ErrorContains(t, err, "does not match journal manifest")
	assert.Empty(t, client.puts)
	assert.Empty(t, registry.states)
}

func TestSyncRejectsCOSHeadMismatchBeforeRegistryAndJournal(t *testing.T) {
	root, store, _, registry := newSyncFixture(t)
	pastMonth := time.Now().UTC().AddDate(0, -2, 0).Format("200601")
	key, _ := materializePartition(t, store, root, "venue:okx", pastMonth)
	wrong := ObjectMetadata{SHA256: "wrong", Size: 1}
	client := &fakeObjectClient{head: &wrong}
	err := Syncer{
		Client: client, Journal: store, Registry: registry, Root: root,
		PartitionLocks: partitionlock.New(),
	}.Sync(t.Context())
	require.ErrorContains(t, err, "metadata mismatch")
	assert.Empty(t, registry.states)
	state, stateErr := store.PartitionState(t.Context(), key)
	require.NoError(t, stateErr)
	assert.NotEqual(t, "synced", state.COS.Status)
}

func TestSyncRejectsNilDependencies(t *testing.T) {
	require.Error(t, (Syncer{}).Sync(t.Context()))
	require.Error(t, (Syncer{Client: &fakeObjectClient{}}).Sync(t.Context()))
}

func TestSyncPropagatesPutErrorWithoutClosingGeneration(t *testing.T) {
	root, store, _, registry := newSyncFixture(t)
	pastMonth := time.Now().UTC().AddDate(0, -2, 0).Format("200601")
	key, _ := materializePartition(t, store, root, "venue:okx", pastMonth)
	client := &fakeObjectClient{err: assert.AnError}

	err := Syncer{Client: client, Journal: store, Registry: registry, Root: root, Workers: 1, PartitionLocks: partitionlock.New()}.Sync(t.Context())
	require.Error(t, err)
	state, stateErr := store.PartitionState(t.Context(), key)
	require.NoError(t, stateErr)
	assert.NotEqual(t, "synced", state.COS.Status)
	assert.Empty(t, registry.states)
}

func TestSyncSeriesTagPresenceSelectsAllEmptyAndExact(t *testing.T) {
	root, store, _, _ := newSyncFixture(t)
	pastMonth := time.Now().UTC().AddDate(0, -2, 0).Format("200601")
	materializePartition(t, store, root, "", pastMonth)
	materializePartition(t, store, root, "venue:binance", pastMonth)

	for _, test := range []struct {
		name     string
		selector *string
		want     int
	}{
		{name: "all", selector: nil, want: 2},
		{name: "empty", selector: ptr(""), want: 1},
		{name: "exact", selector: ptr("venue:binance"), want: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeObjectClient{}
			registry := &fakeCOSRegistry{}
			err := Syncer{Client: client, Journal: store, Registry: registry, Root: root, SeriesTag: test.selector, Workers: 1, PartitionLocks: partitionlock.New()}.Sync(t.Context())
			require.NoError(t, err)
			require.Len(t, client.puts, test.want)
			require.Len(t, registry.states, test.want)
		})
	}
}

func TestSyncSkipsCurrentMonthWhenDisabled(t *testing.T) {
	root, store, client, registry := newSyncFixture(t)
	materializePartition(t, store, root, "venue:binance", time.Now().UTC().Format("200601"))
	err := Syncer{Client: client, Journal: store, Registry: registry, Root: root, SyncOpenPartitions: false, PartitionLocks: partitionlock.New()}.Sync(t.Context())
	require.NoError(t, err)
	assert.Empty(t, client.puts)
}

func TestSyncAndWriterSerializeOnePartitionGeneration(t *testing.T) {
	root, store, _, _ := newSyncFixture(t)
	pastMonth := time.Now().UTC().AddDate(0, -2, 0).Format("200601")
	key, _ := materializePartition(t, store, root, "venue:binance", pastMonth)
	locks := partitionlock.New()
	registry := &generationRegistry{}
	putStarted := make(chan struct{})
	releasePut := make(chan struct{})
	client := &fakeObjectClient{onPut: func() {
		close(putStarted)
		<-releasePut
	}}
	syncErr := make(chan error, 1)
	go func() {
		syncErr <- (Syncer{
			Client: client, Journal: store, Registry: registry, Root: root,
			Workers: 1, PartitionLocks: locks,
		}).Sync(t.Context())
	}()
	<-putStarted

	revised := 2.5
	dataTime, err := time.Parse("200601", key.Month)
	require.NoError(t, err)
	_, err = store.Append(t.Context(), domain.EventBatch{
		MessageID: "revision-during-cos",
		Rows: []domain.RowPatch{{
			Partition: key, DataTime: dataTime,
			WrittenAt: time.Now().UTC(),
			Columns:   map[string]domain.Scalar{"close": {Type: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Double: &revised}},
		}},
	})
	require.NoError(t, err)
	w := writer.New(store, root, 1024)
	w.SetPartitionLocker(locks)
	w.SetRegistry(registry)
	writerDone := make(chan error, 1)
	go func() {
		_, writeErr := w.WritePartition(t.Context(), key)
		writerDone <- writeErr
	}()
	select {
	case err := <-writerDone:
		t.Fatalf("writer crossed active COS partition lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(releasePut)
	require.NoError(t, <-syncErr)
	require.NoError(t, <-writerDone)
	require.Equal(t, []uint64{1, 2}, registry.generations)
	state, err := store.PartitionState(t.Context(), key)
	require.NoError(t, err)
	require.Equal(t, uint64(2), state.Manifest.Generation)
}

func ptr(value string) *string { return &value }
