package pebble

import (
	"context"
	"errors"
	"strings"
	"time"

	cpebble "github.com/cockroachdb/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"

	"trpc.group/trpc-go/trpc-go/log"
)

// writeLogThreshold 是写入分段耗时日志的阈值。
const writeLogThreshold = 50 * time.Millisecond

// UpsertFields 写入一个数据集的行：字段、属性、行索引与变更日志在同一个 Pebble 批次里提交。不去重，同一行重复写入时后写覆盖先写。
func (s *Store) UpsertFields(ctx context.Context, spaceID, datasetID string, rows []*pb.RowFieldUpsert) error {
	if strings.TrimSpace(spaceID) == "" || strings.TrimSpace(datasetID) == "" {
		return invalid("space_id and dataset_id are required")
	}
	// 分段计时：规整、准备（字段、行索引与变更范围）、等写锁、提交；达到阈值时记一行。
	started := time.Now()
	var normalizedAt, preparedAt, lockedAt, committedAt time.Time
	defer func() {
		if committedAt.IsZero() || time.Since(started) < writeLogThreshold {
			return
		}
		log.Infof("storage 写入 dataset=%s 行=%d 规整=%s 准备=%s 等锁=%s 提交=%s 合计=%s",
			datasetID, len(rows), normalizedAt.Sub(started).Round(time.Millisecond), preparedAt.Sub(normalizedAt).Round(time.Millisecond),
			lockedAt.Sub(preparedAt).Round(time.Millisecond), committedAt.Sub(lockedAt).Round(time.Millisecond), time.Since(started).Round(time.Millisecond))
	}()
	normalized, err := s.normalizeWriteRows(ctx, rows)
	if err != nil {
		return err
	}
	for _, row := range normalized {
		if row.GetKey().GetSpaceId() != spaceID || row.GetKey().GetDatasetId() != datasetID {
			return invalid("row dataset identity does not match request")
		}
	}
	normalizedAt = time.Now()

	s.datasetWriteMu.RLock()
	defer s.datasetWriteMu.RUnlock()
	if err := s.requireDataset(spaceID, datasetID); err != nil {
		return err
	}
	// 编码 KV、整理变更范围与锁无关，放在 outboxMu 之外：并发写入与完成标记不必排在这一步后面。锁内只提交。
	write, err := s.prepareRowsWrite(ctx, spaceID, datasetID, normalized)
	if err != nil {
		return err
	}
	defer write.batch.Close()
	preparedAt = time.Now()
	s.outboxMu.Lock()
	defer s.outboxMu.Unlock()
	lockedAt = time.Now()
	if err := s.commitRowsWriteLocked(ctx, write, nil); err != nil {
		return err
	}
	committedAt = time.Now()
	return nil
}

func (s *Store) normalizeWriteRows(ctx context.Context, rows []*pb.RowFieldUpsert) ([]*pb.RowFieldUpsert, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("pebble store is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, invalid("rows are required")
	}
	normalizedRows := make([]*pb.RowFieldUpsert, 0, len(rows))
	for _, row := range rows {
		if err := validateUpsert(row); err != nil {
			return nil, err
		}
		key, err := NormalizeRowKey(row.GetKey())
		if err != nil {
			return nil, err
		}
		clone := proto.Clone(row).(*pb.RowFieldUpsert)
		clone.Key = key
		normalizedRows = append(normalizedRows, clone)
	}
	return normalizedRows, nil
}

// preparedRowsWrite 是一次写入在锁外准备好的部分：已写入字段、属性与行索引 KV 的 batch，以及变更日志的时间范围与记录键。
type preparedRowsWrite struct {
	batch     *cpebble.Batch
	spaceID   string
	datasetID string
	ranges    []*pb.DatasetChangeRange
	records   []*pb.RecordRowKey
}

// prepareRowsWrite 只读 Store 的静态配置，不读写变更序号，可以在 outboxMu 之外并发执行。
func (s *Store) prepareRowsWrite(ctx context.Context, spaceID, datasetID string, rows []*pb.RowFieldUpsert) (*preparedRowsWrite, error) {
	// One batch preserves all field changes from a request. The change log is
	// staged into the same batch.
	batch := s.db.NewBatch()
	fail := func(err error) (*preparedRowsWrite, error) {
		_ = batch.Close()
		return nil, err
	}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		// Keep a single logical row marker alongside the field/attribute values.
		// The marker is an ordered, time-first index used by history backfills;
		// writing it in the same batch makes a committed row immediately visible
		// to the history reader.
		if row.GetKey().GetTimeSeries() != nil {
			historyKey, err := encodeHistoryKey(row.GetKey())
			if err != nil {
				return fail(err)
			}
			if err := batch.Set(historyKey, nil, s.writeOptions); err != nil {
				return fail(err)
			}
			seriesHistoryKey, err := encodeSeriesHistoryKey(row.GetKey())
			if err != nil {
				return fail(err)
			}
			if err := batch.Set(seriesHistoryKey, nil, s.writeOptions); err != nil {
				return fail(err)
			}
		}
		for _, field := range row.GetFields() {
			key, err := encodeFieldKey(row.GetKey(), field.GetFieldId())
			if err != nil {
				return fail(err)
			}
			data, err := proto.Marshal(field.GetValue())
			if err != nil {
				return fail(err)
			}
			if err := batch.Set(key, data, s.writeOptions); err != nil {
				return fail(err)
			}
		}
		for name, value := range row.GetAttributes() {
			key, err := encodeAttributeKey(row.GetKey(), name)
			if err != nil {
				return fail(err)
			}
			data, err := proto.Marshal(value)
			if err != nil {
				return fail(err)
			}
			if err := batch.Set(key, data, s.writeOptions); err != nil {
				return fail(err)
			}
		}
	}
	return &preparedRowsWrite{batch: batch, spaceID: spaceID, datasetID: datasetID, ranges: changeRanges(rows), records: changeRecords(rows)}, nil
}

// commitRowsWriteLocked 在 outboxMu 内完成写入：追加变更日志，由 decorate 补充同一批次里的其他键
// （周期账本），然后提交。调用方必须持有 outboxMu（它同时保护变更日志的序号）。
func (s *Store) commitRowsWriteLocked(ctx context.Context, prepared *preparedRowsWrite, decorate func(*cpebble.Batch) error) error {
	// 等锁期间请求可能已超时或取消：此时不再提交，客户端重试时不会重复写入。
	if err := ctx.Err(); err != nil {
		return err
	}
	batch := prepared.batch
	if err := s.stageRowsChangeLocked(batch, prepared.spaceID, prepared.datasetID, prepared.ranges, prepared.records); err != nil {
		return err
	}
	if decorate != nil {
		if err := decorate(batch); err != nil {
			return err
		}
	}
	if err := batch.Commit(s.writeOptions); err != nil {
		return err
	}
	s.noteChangeCommitted()
	return nil
}
