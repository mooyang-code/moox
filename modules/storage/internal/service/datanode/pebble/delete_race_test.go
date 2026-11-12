package pebble

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// 删除与写入交错：写入已经拿到读锁、确认数据集还在时，删除先删了元数据并等写锁；
// 写入完成后删除才删行，写入的行随之消失；删除之后进来的写入按最新元数据被拒绝。
func TestDeleteDatasetRowsRemovesRowsOfAWriteAdmittedBeforeIt(t *testing.T) {
	var exists atomic.Bool
	exists.Store(true)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "db"), NodeID: "node-1", DatasetExists: func(string, string) bool {
		ok := exists.Load()
		once.Do(func() {
			close(entered)
			<-release
		})
		return ok
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	row := &pb.RowFieldUpsert{
		Key:    &pb.RowKey{SpaceId: "s", DatasetId: "racing", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}},
		Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "v"}}}},
	}

	writeErr := make(chan error, 1)
	go func() {
		_, err := store.UpsertFields(ctx, "s", "racing", "", []*pb.RowFieldUpsert{row})
		writeErr <- err
	}()
	<-entered
	// 写入卡在存在性检查里（持有读锁）：元数据此时被删掉，删除在写锁前等待。
	exists.Store(false)
	deleteErr := make(chan error, 1)
	go func() {
		_, err := store.DeleteDatasetRows(ctx, "s", "racing")
		deleteErr <- err
	}()
	select {
	case err := <-deleteErr:
		t.Fatalf("delete finished while a write held the read lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-writeErr; err != nil {
		t.Fatalf("write admitted before the delete: %v", err)
	}
	if err := <-deleteErr; err != nil {
		t.Fatal(err)
	}
	rows, err := store.ReadRows(ctx, []*pb.RowKey{row.GetKey()}, []string{"value"}, nil)
	if err != nil || len(rows) != 0 {
		t.Fatalf("rows after delete=%v err=%v, want the admitted write removed", rows, err)
	}
	if _, err := store.UpsertFields(ctx, "s", "racing", "", []*pb.RowFieldUpsert{row}); !errors.Is(err, ErrDatasetDeleted) {
		t.Fatalf("write after delete err=%v, want ErrDatasetDeleted", err)
	}
}

func TestErrorCodeClassifiesStorageErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want pb.ErrorCode
	}{
		{"nil", nil, pb.ErrorCode_SUCCESS},
		{"plain", errors.New("disk failure"), pb.ErrorCode_INNER_ERR},
		{"dataset deleted", ErrDatasetDeleted, pb.ErrorCode_INNER_ERR},
		{"validation", ValidationErrorFor("bad"), pb.ErrorCode_INVALID_PARAM},
		{"wrapped validation", errors.Join(errors.New("ctx"), ValidationErrorFor("bad")), pb.ErrorCode_INVALID_PARAM},
		{"deadline passed", PeriodDeadlinePassedError{Deadline: 1}, pb.ErrorCode_INVALID_PARAM},
		{"marker conflict", ConflictError{EventID: "e"}, pb.ErrorCode_CONFLICT},
		{"commit conflict", CommitConflictError{CommitID: "c"}, pb.ErrorCode_CONFLICT},
		{"period conflict", PeriodConflictError{}, pb.ErrorCode_CONFLICT},
		{"stale effective_from", StaleEffectiveFromError{}, pb.ErrorCode_CONFLICT},
		{"hash mismatch", PeriodHashMismatchError{}, pb.ErrorCode_CONFLICT},
		{"snapshot missing", SeriesSnapshotMissingError{}, pb.ErrorCode_NOT_FOUND},
	} {
		if got := ErrorCode(tc.err); got != tc.want {
			t.Errorf("%s: ErrorCode=%s want %s", tc.name, got, tc.want)
		}
	}
}
