package pebble

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	cpebble "github.com/cockroachdb/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	frequencypkg "github.com/mooyang-code/moox/packages/frequency"
	storageeventpb "github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"trpc.group/trpc-go/trpc-go/log"
)

// 周期账本：每期一条记录，键为 __period/{空间}/{数据集}/{频率}/{周期}，各段与快照存储一样按 hex 编码，周期为 20 位十进制。
const (
	periodPrefix = "__period/"

	periodWaiting  = "waiting"
	periodComplete = "complete"
	periodDegraded = "degraded"

	// periodFinalizeMargin 是窗口扫描在“截止时间上限”之外多看的一段，覆盖后台判定偶尔慢一拍的情况。
	periodFinalizeMargin = time.Hour
)

type DatasetPeriodSeries struct {
	SeriesIndex uint32 `json:"series_index"`
	SubjectID   string `json:"subject_id"`
	SeriesTag   string `json:"series_tag"`
}

// DatasetPeriodExpectation 是一次周期提交声明的周期身份；DeadlineAt 与 SeriesSnapshot 由 Storage 按生效快照补全。
type DatasetPeriodExpectation struct {
	SpaceID        string
	DatasetID      string
	Frequency      string
	PeriodTime     int64
	SeriesHash     string
	ExpectedCount  uint32
	DeadlineAt     int64
	SeriesSnapshot []DatasetPeriodSeries
	// AllowedLatenessSeconds 只在周期不存在、由提交自动建立时使用，决定新周期的截止时间。
	AllowedLatenessSeconds int64
}

type TimeSeriesBatchItem struct {
	SeriesIndex uint32
	Row         *pb.RowFieldUpsert
}

type DatasetPeriodResult struct {
	Status                string
	DeadlineAt            int64
	AcceptedSeriesIndexes []uint32
}

// periodRecord 是账本里一期的记录：状态、快照引用（series_hash）、序列数、截止时间与成功位图。
type periodRecord struct {
	Status        string `json:"status"`
	SeriesHash    string `json:"series_hash"`
	ExpectedCount uint32 `json:"expected_count"`
	DeadlineAt    int64  `json:"deadline_at"`
	Success       []byte `json:"success"`
}

// due 判断等待中的记录是否该判定：序列已齐，或已到截止时间。
func (r periodRecord) due(now time.Time) bool {
	return r.Status == periodWaiting && (bitmapAllSet(r.Success, r.ExpectedCount) || periodDeadlineReached(r.DeadlineAt, now))
}

func periodDeadlineReached(deadline int64, now time.Time) bool {
	return deadline > 0 && now.Unix() >= deadline
}

// datasetScope 是数据集一级的键段：hex(空间)/hex(数据集)/。
func datasetScope(spaceID, datasetID string) string {
	return hex.EncodeToString([]byte(spaceID)) + "/" + hex.EncodeToString([]byte(datasetID)) + "/"
}

func periodDatasetPrefix(spaceID, datasetID string) string {
	return periodPrefix + datasetScope(spaceID, datasetID)
}

func periodScopePrefix(spaceID, datasetID, frequency string) string {
	return periodPrefix + snapshotScope(spaceID, datasetID, frequency)
}

func periodKey(spaceID, datasetID, frequency string, periodTime int64) []byte {
	return []byte(periodScopePrefix(spaceID, datasetID, frequency) + periodTimePart(periodTime))
}

func periodTimePart(periodTime int64) string { return fmt.Sprintf("%020d", periodTime) }

// parseScopeKey 拆开 {prefix}{hex 空间}/{hex 数据集}/{hex 频率}/{周期} 开头的键，返回到频率一级的前缀与周期之后的剩余部分。
func parseScopeKey(prefix string, key []byte) (spaceID, datasetID, frequency string, periodTime int64, rest string, err error) {
	if !bytes.HasPrefix(key, []byte(prefix)) {
		return "", "", "", 0, "", fmt.Errorf("键 %q 不在 %s 下", key, prefix)
	}
	parts := strings.SplitN(string(key[len(prefix):]), "/", 5)
	if len(parts) < 4 {
		return "", "", "", 0, "", fmt.Errorf("键 %q 格式不对", key)
	}
	decoded := make([]string, 3)
	for i := range decoded {
		value, decodeErr := hex.DecodeString(parts[i])
		if decodeErr != nil {
			return "", "", "", 0, "", fmt.Errorf("键 %q 格式不对: %w", key, decodeErr)
		}
		decoded[i] = string(value)
	}
	periodTime, err = strconv.ParseInt(parts[3], 10, 64)
	if err != nil {
		return "", "", "", 0, "", fmt.Errorf("键 %q 的周期不对: %w", key, err)
	}
	if len(parts) == 5 {
		rest = parts[4]
	}
	return decoded[0], decoded[1], decoded[2], periodTime, rest, nil
}

func (s *Store) readPeriodRecord(key []byte) (periodRecord, bool, error) {
	value, err := s.readPeriodValue(key)
	if errors.Is(err, cpebble.ErrNotFound) {
		return periodRecord{}, false, nil
	}
	if err != nil {
		return periodRecord{}, false, err
	}
	record, err := decodePeriodRecord(value)
	return record, err == nil, err
}

func decodePeriodRecord(value []byte) (periodRecord, error) {
	var record periodRecord
	if err := json.Unmarshal(value, &record); err != nil {
		return periodRecord{}, fmt.Errorf("周期账本记录已损坏: %w", err)
	}
	if len(record.Success) != bitmapSize(record.ExpectedCount) {
		return periodRecord{}, errors.New("周期账本记录的成功位图长度不对")
	}
	return record, nil
}

func (s *Store) validatePeriodUniverseSize(exp DatasetPeriodExpectation) error {
	if s.maxEventBytes <= 0 {
		return nil
	}
	// 最坏情况：每个标的、每条序列都失败。统一数据集的序列名单（标的加 series_tag）出现两次（全集与失败名单）。
	universe, _ := periodMarkerSubjects(exp, nil)
	universeSeries, _ := periodMarkerSeries(exp, nil)
	completed := &LedgerPeriodCompleted{
		DatasetID: exp.DatasetID, Frequency: exp.Frequency, PeriodTime: exp.PeriodTime,
		Status: periodDegraded, SnapshotRef: exp.SeriesHash,
		UniverseSubjectIDs: universe, FailedSubjects: append([]string(nil), universe...),
		UniverseSeries: universeSeries, FailedSeries: universeSeries,
		CompletedAt: timestamppb.New(time.Date(9999, time.December, 31, 23, 59, 59, 999999999, time.UTC)),
	}
	raw, _, err := BuildLedgerPeriodCompletedMessage(exp.SpaceID, completed)
	if err != nil {
		return err
	}
	return s.validatePeriodMarkerPayloadSize(raw)
}

func (s *Store) validatePeriodMarkerPayloadSize(raw []byte) error {
	if s.maxEventBytes > 0 && len(raw) > s.maxEventBytes {
		return invalidf("event payload size %d exceeds limit %d", len(raw), s.maxEventBytes)
	}
	return nil
}

// CommitTimeSeriesBatch 写入一期的行并更新账本：周期不存在时按生效快照建立；全部序列到齐即判定完成。
// 已终态或已过截止的周期不再写行，只返回终态（过截止的等待周期在这里判定为降级）。
func (s *Store) CommitTimeSeriesBatch(ctx context.Context, exp DatasetPeriodExpectation, items []TimeSeriesBatchItem) (DatasetPeriodResult, error) {
	exp, err := normalizePeriodExpectation(exp)
	if err != nil {
		return DatasetPeriodResult{}, err
	}
	if len(items) == 0 {
		return DatasetPeriodResult{}, invalid("time-series batch items are required")
	}
	rows := make([]*pb.RowFieldUpsert, 0, len(items))
	for _, item := range items {
		if item.Row == nil {
			return DatasetPeriodResult{}, invalid("period batch row is required")
		}
		rows = append(rows, item.Row)
	}
	normalizedRows, err := s.normalizeWriteRows(ctx, rows)
	if err != nil {
		return DatasetPeriodResult{}, err
	}
	key := periodKey(exp.SpaceID, exp.DatasetID, exp.Frequency, exp.PeriodTime)
	s.datasetWriteMu.RLock()
	defer s.datasetWriteMu.RUnlock()
	if err := s.requireDataset(exp.SpaceID, exp.DatasetID); err != nil {
		return DatasetPeriodResult{}, err
	}
	s.periodMu.Lock()
	defer s.periodMu.Unlock()
	record, found, err := s.readPeriodRecord(key)
	if err != nil {
		return DatasetPeriodResult{}, err
	}
	now := s.periodNow().UTC()
	if !found {
		// 周期不存在：按生效表与已登记快照建立（带来的 hash 必须正是该周期应生效的 hash）。
		if exp, err = s.planLazyPeriod(exp, exp.AllowedLatenessSeconds, now); err != nil {
			return DatasetPeriodResult{}, err
		}
		if err := s.validatePeriodUniverseSize(exp); err != nil {
			return DatasetPeriodResult{}, err
		}
		record = periodRecord{Status: periodWaiting, SeriesHash: exp.SeriesHash, ExpectedCount: exp.ExpectedCount, DeadlineAt: exp.DeadlineAt, Success: make([]byte, bitmapSize(exp.ExpectedCount))}
	} else {
		if record.SeriesHash != exp.SeriesHash || record.ExpectedCount != exp.ExpectedCount {
			return DatasetPeriodResult{}, PeriodConflictError{SpaceID: exp.SpaceID, DatasetID: exp.DatasetID, Frequency: exp.Frequency, PeriodTime: exp.PeriodTime}
		}
		if exp.SeriesSnapshot, err = s.loadSeriesSnapshot(exp.SpaceID, exp.DatasetID, exp.Frequency, record.SeriesHash); err != nil {
			return DatasetPeriodResult{}, err
		}
		exp.DeadlineAt = record.DeadlineAt
	}
	targets := make([]byte, bitmapSize(exp.ExpectedCount))
	for i, item := range items {
		isTarget, err := validatePeriodRow(exp, item.SeriesIndex, normalizedRows[i])
		if err != nil {
			return DatasetPeriodResult{}, err
		}
		if isTarget {
			setBitmapBit(targets, item.SeriesIndex)
		}
	}
	if record.Status != periodWaiting || periodDeadlineReached(record.DeadlineAt, now) {
		if record, err = s.finalizePeriodLocked(ctx, exp, record, now); err != nil {
			return DatasetPeriodResult{}, err
		}
		return periodCommitReceipt(record, targets), nil
	}
	for index := uint32(0); index < exp.ExpectedCount; index++ {
		if bitmapBitSet(targets, index) {
			setBitmapBit(record.Success, index)
		}
	}
	recordRaw, err := json.Marshal(record)
	if err != nil {
		return DatasetPeriodResult{}, err
	}
	// 行的准备（编码 KV、构造事件、整理变更范围）不依赖 outbox 状态，放在 outboxMu 之外；周期状态仍由 periodMu 保护。
	write, err := s.prepareRowsWrite(ctx, exp.SpaceID, exp.DatasetID, normalizedRows)
	if err != nil {
		return DatasetPeriodResult{}, err
	}
	defer write.batch.Close()
	s.outboxMu.Lock()
	_, err = s.commitRowsWriteLocked(ctx, write, func(batch *cpebble.Batch) error {
		return batch.Set(key, recordRaw, s.writeOptions)
	})
	s.outboxMu.Unlock()
	if err != nil {
		return DatasetPeriodResult{}, err
	}
	if record, err = s.finalizePeriodLocked(ctx, exp, record, now); err != nil {
		return DatasetPeriodResult{}, err
	}
	return periodCommitReceipt(record, targets), nil
}

// periodCommitReceipt 返回本次提交里目标周期已成功的序号。
func periodCommitReceipt(record periodRecord, targets []byte) DatasetPeriodResult {
	result := DatasetPeriodResult{Status: record.Status, DeadlineAt: record.DeadlineAt}
	for index := uint32(0); index < record.ExpectedCount; index++ {
		if bitmapBitSet(targets, index) && bitmapBitSet(record.Success, index) {
			result.AcceptedSeriesIndexes = append(result.AcceptedSeriesIndexes, index)
		}
	}
	return result
}

// finalizePeriodLocked 判定一期：序列齐了是完成，过了截止是降级，否则仍等待。判定出终态时，
// 账本状态、完成标记与 Outbox 条目在同一个批次里提交。exp 须带完整快照。
// 调用方持有 datasetWriteMu 读锁与 periodMu，这里只再取 outboxMu。
func (s *Store) finalizePeriodLocked(ctx context.Context, exp DatasetPeriodExpectation, record periodRecord, now time.Time) (periodRecord, error) {
	if err := ctx.Err(); err != nil {
		return record, err
	}
	if record.Status != periodWaiting {
		return record, nil
	}
	terminal := ""
	switch {
	case bitmapAllSet(record.Success, record.ExpectedCount):
		terminal = periodComplete
	case periodDeadlineReached(record.DeadlineAt, now):
		terminal = periodDegraded
	default:
		return record, nil
	}
	universe, failed := periodMarkerSubjects(exp, record.Success)
	universeSeries, failedSeries := periodMarkerSeries(exp, record.Success)
	raw, _, err := BuildLedgerPeriodCompletedMessage(exp.SpaceID, &LedgerPeriodCompleted{
		DatasetID: exp.DatasetID, Frequency: exp.Frequency, PeriodTime: exp.PeriodTime,
		Status: terminal, SnapshotRef: record.SeriesHash,
		UniverseSubjectIDs: universe, FailedSubjects: failed,
		UniverseSeries: universeSeries, FailedSeries: failedSeries, CompletedAt: timestamppb.New(now.UTC()),
	})
	if err != nil {
		return record, err
	}
	if err := s.validatePeriodMarkerPayloadSize(raw); err != nil {
		return record, err
	}
	message := &eventpb.EventMessage{}
	if err := proto.Unmarshal(raw, message); err != nil {
		return record, err
	}
	finalized := record
	finalized.Status = terminal
	recordRaw, err := json.Marshal(finalized)
	if err != nil {
		return record, err
	}
	s.outboxMu.Lock()
	defer s.outboxMu.Unlock()
	batch := s.db.NewBatch()
	defer batch.Close()
	if err := batch.Set(periodKey(exp.SpaceID, exp.DatasetID, exp.Frequency, exp.PeriodTime), recordRaw, s.writeOptions); err != nil {
		return record, err
	}
	written, err := s.stageDatasetMarkerLocked(batch, ledgerMarkerKey(exp.SpaceID, exp.DatasetID, exp.Frequency, exp.PeriodTime), message, raw)
	if err != nil {
		return record, err
	}
	if err := batch.Commit(s.writeOptions); err != nil {
		return record, err
	}
	if written {
		s.noteOutboxCommitted(1, now.UTC())
	}
	return finalized, nil
}

// finalizePeriod 判定一期等待中的记录，供截止扫描使用。
func (s *Store) finalizePeriod(ctx context.Context, key []byte, now time.Time) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	spaceID, datasetID, frequency, periodTime, _, err := parseScopeKey(periodPrefix, key)
	if err != nil {
		return "", err
	}
	s.datasetWriteMu.RLock()
	defer s.datasetWriteMu.RUnlock()
	s.periodMu.Lock()
	defer s.periodMu.Unlock()
	record, found, err := s.readPeriodRecord(key)
	if err != nil || !found {
		return "", err
	}
	if !record.due(now) {
		return record.Status, nil
	}
	snapshot, err := s.loadSeriesSnapshot(spaceID, datasetID, frequency, record.SeriesHash)
	if err != nil {
		return "", err
	}
	exp := DatasetPeriodExpectation{
		SpaceID: spaceID, DatasetID: datasetID, Frequency: frequency, PeriodTime: periodTime,
		SeriesHash: record.SeriesHash, ExpectedCount: record.ExpectedCount, DeadlineAt: record.DeadlineAt, SeriesSnapshot: snapshot,
	}
	record, err = s.finalizePeriodLocked(ctx, exp, record, now)
	return record.Status, err
}

// startPeriodFinalizer 每秒扫一遍未完成的周期并判定。启动后第一遍扫全部记录（停机期间到期的周期要补判定），
// 之后每个（数据集、频率）只扫最近一段：等待中的周期截止时间不晚于收盘后 24 小时。
func (s *Store) startPeriodFinalizer() {
	if s == nil || s.backgroundCtx == nil {
		return
	}
	s.maintenanceWG.Add(1)
	go func() {
		defer s.maintenanceWG.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		windowed := false
		for {
			select {
			case <-s.backgroundCtx.Done():
				return
			case <-ticker.C:
				if _, err := s.finalizeDuePeriods(s.backgroundCtx, s.periodNow().UTC(), windowed); err != nil {
					s.logPeriodFinalizeError(err)
				} else {
					s.clearPeriodFinalizeError()
				}
				windowed = true
			}
		}
	}()
}

func (s *Store) logPeriodFinalizeError(err error) {
	if err == nil {
		return
	}
	message := err.Error()
	s.periodFinalizeErrorMu.Lock()
	defer s.periodFinalizeErrorMu.Unlock()
	if message == s.periodFinalizeLastError {
		return
	}
	s.periodFinalizeLastError = message
	log.Errorf("storage 周期截止判定失败: %v", err)
}

func (s *Store) clearPeriodFinalizeError() {
	s.periodFinalizeErrorMu.Lock()
	s.periodFinalizeLastError = ""
	s.periodFinalizeErrorMu.Unlock()
}

// FinalizeWaitingDatasetPeriods 扫全部账本记录，判定到期的周期（序列已齐或已过截止），返回判定出终态的期数。
// 某一期判定失败不影响其他期，错误合并返回。
func (s *Store) FinalizeWaitingDatasetPeriods(ctx context.Context, now time.Time) (int, error) {
	return s.finalizeDuePeriods(ctx, now, false)
}

func (s *Store) finalizeDuePeriods(ctx context.Context, now time.Time, windowed bool) (int, error) {
	if now.IsZero() {
		now = s.periodNow().UTC()
	}
	keys, err := s.duePeriodKeys(ctx, now, windowed)
	if err != nil {
		return 0, err
	}
	finalized := 0
	var finalizeErrors []error
	for _, key := range keys {
		status, err := s.finalizePeriod(ctx, key, now)
		if err != nil {
			finalizeErrors = append(finalizeErrors, fmt.Errorf("判定周期 %q: %w", key, err))
			continue
		}
		if status == periodComplete || status == periodDegraded {
			finalized++
		}
	}
	return finalized, errors.Join(finalizeErrors...)
}

// duePeriodKeys 列出到期的等待记录。windowed 时每个（数据集、频率）直接跳到最近一段，不读更早的终态记录。
func (s *Store) duePeriodKeys(ctx context.Context, now time.Time, windowed bool) ([][]byte, error) {
	iter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: []byte(periodPrefix), UpperBound: nextPrefix([]byte(periodPrefix))})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var due [][]byte
	scope := ""
	for valid := iter.First(); valid; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		key := iter.Key()
		slash := bytes.LastIndexByte(key, '/')
		keyScope := string(key[:slash+1])
		if windowed && keyScope != scope {
			scope = keyScope
			if lower := periodWindowLower(keyScope, now); lower != nil && bytes.Compare(key, lower) < 0 {
				valid = iter.SeekGE(lower)
				continue
			}
		}
		scope = keyScope
		record, err := decodePeriodRecord(iter.Value())
		if err != nil {
			return nil, fmt.Errorf("周期 %q: %w", key, err)
		}
		if record.due(now) {
			due = append(due, append([]byte(nil), key...))
		}
		valid = iter.Next()
	}
	return due, iter.Error()
}

// periodWindowLower 返回窗口扫描在一个（数据集、频率）下的起点：更早的周期截止时间早已过去，已经判定过。
// 频率解析不了时返回 nil，扫这个范围的全部记录。
func periodWindowLower(scope string, now time.Time) []byte {
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(scope, periodPrefix), "/"), "/")
	if len(parts) != 3 {
		return nil
	}
	raw, err := hex.DecodeString(parts[2])
	if err != nil {
		return nil
	}
	frequency, err := frequencypkg.Parse(string(raw))
	if err != nil {
		return nil
	}
	lookback := 2*frequency.NominalDuration() + maxAllowedLatenessSeconds*time.Second + periodFinalizeMargin
	from := now.Add(-lookback).Unix()
	if from <= 0 {
		return nil
	}
	return []byte(scope + periodTimePart(from))
}

// stageDeleteBeforePeriod 在 batch 里删掉 datasetPrefix（到数据集一级）下各频率中周期早于 before 的键，返回删除的范围。
// 账本与完成标记都是“数据集/频率/周期”的布局，频率夹在中间，所以按频率逐段删除。
func (s *Store) stageDeleteBeforePeriod(batch *cpebble.Batch, datasetPrefix string, before int64) ([][2][]byte, error) {
	var ranges [][2][]byte
	err := s.eachFrequencyPrefix(datasetPrefix, func(frequencyPrefix string) error {
		lower, upper := []byte(frequencyPrefix), []byte(frequencyPrefix+periodTimePart(before))
		if err := batch.DeleteRange(lower, upper, s.writeOptions); err != nil {
			return err
		}
		ranges = append(ranges, [2][]byte{lower, upper})
		return nil
	})
	return ranges, err
}

// eachFrequencyPrefix 对 datasetPrefix 下实际存在的每个频率前缀（{datasetPrefix}{hex 频率}/）调一次 fn，逐个频率跳过。
func (s *Store) eachFrequencyPrefix(datasetPrefix string, fn func(frequencyPrefix string) error) error {
	iter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: []byte(datasetPrefix), UpperBound: nextPrefix([]byte(datasetPrefix))})
	if err != nil {
		return err
	}
	defer iter.Close()
	for valid := iter.First(); valid; {
		rest := iter.Key()[len(datasetPrefix):]
		slash := bytes.IndexByte(rest, '/')
		if slash < 0 {
			valid = iter.Next()
			continue
		}
		frequencyPrefix := datasetPrefix + string(rest[:slash+1])
		if err := fn(frequencyPrefix); err != nil {
			return err
		}
		valid = iter.SeekGE(nextPrefix([]byte(frequencyPrefix)))
	}
	return iter.Error()
}

func normalizePeriodExpectation(exp DatasetPeriodExpectation) (DatasetPeriodExpectation, error) {
	exp.SpaceID = strings.TrimSpace(exp.SpaceID)
	exp.DatasetID = strings.TrimSpace(exp.DatasetID)
	exp.Frequency = strings.TrimSpace(exp.Frequency)
	exp.SeriesHash = strings.TrimSpace(exp.SeriesHash)
	if exp.SpaceID == "" || exp.DatasetID == "" || exp.Frequency == "" || exp.PeriodTime <= 0 || exp.SeriesHash == "" || exp.ExpectedCount == 0 {
		return exp, invalid("space_id, dataset_id, frequency, period_time, series_hash and expected_count are required")
	}
	return exp, nil
}

func validatePeriodRow(exp DatasetPeriodExpectation, seriesIndex uint32, row *pb.RowFieldUpsert) (bool, error) {
	if err := validatePeriodSeriesIndex(exp, seriesIndex); err != nil {
		return false, err
	}
	if row == nil || row.GetKey() == nil || row.GetKey().GetTimeSeries() == nil {
		return false, invalid("period batch rows must be time-series rows")
	}
	key := row.GetKey()
	series := key.GetTimeSeries()
	if key.GetSpaceId() != exp.SpaceID || key.GetDatasetId() != exp.DatasetID || series.GetFreq() != exp.Frequency {
		return false, invalid("period batch row identity does not match expectation")
	}
	if series.GetSubjectId() != exp.SeriesSnapshot[seriesIndex].SubjectID || series.GetSeriesTag() != exp.SeriesSnapshot[seriesIndex].SeriesTag {
		return false, invalidf("series_index %d subject_id or series_tag does not match the period series snapshot", seriesIndex)
	}
	at, err := time.Parse(time.RFC3339Nano, series.GetDataTime())
	if err != nil {
		return false, invalidf("period batch data_time is invalid: %v", err)
	}
	target := time.Unix(exp.PeriodTime, 0).UTC()
	if at.Unix() == exp.PeriodTime && !at.Equal(target) {
		return false, invalid("period batch target data_time must match the exact period timestamp")
	}
	return at.Equal(target), nil
}

func validatePeriodSeriesIndex(exp DatasetPeriodExpectation, index uint32) error {
	if len(exp.SeriesSnapshot) != int(exp.ExpectedCount) || index >= exp.ExpectedCount || exp.SeriesSnapshot[index].SeriesIndex != index || exp.SeriesSnapshot[index].SubjectID == "" {
		return invalidf("series_index %d is not in the period series snapshot", index)
	}
	return nil
}

func (s *Store) readPeriodValue(key []byte) ([]byte, error) {
	value, closer, err := s.db.Get(key)
	if err != nil {
		return nil, err
	}
	copyValue := append([]byte(nil), value...)
	if err := closer.Close(); err != nil {
		return nil, err
	}
	return copyValue, nil
}

// periodMarkerSubjects 返回按标的的全集与失败名单：没有成功提交的序列（含从未触发的）所在标的即失败，不要求调用方另报失败。
func periodMarkerSubjects(exp DatasetPeriodExpectation, success []byte) ([]string, []string) {
	universe := make([]string, 0, len(exp.SeriesSnapshot))
	failed := make([]string, 0)
	universeSeen := make(map[string]struct{}, len(exp.SeriesSnapshot))
	failedSeen := make(map[string]struct{})
	for index, series := range exp.SeriesSnapshot {
		subjectID := strings.TrimSpace(series.SubjectID)
		if subjectID == "" {
			continue
		}
		if _, exists := universeSeen[subjectID]; !exists {
			universeSeen[subjectID] = struct{}{}
			universe = append(universe, subjectID)
		}
		if !bitmapBitSet(success, uint32(index)) {
			if _, exists := failedSeen[subjectID]; !exists {
				failedSeen[subjectID] = struct{}{}
				failed = append(failed, subjectID)
			}
		}
	}
	return universe, failed
}

// periodMarkerSeries 返回按序列（标的、series_tag）的期望全集与失败名单。
// 只有快照含多个 series_tag（统一数据集）时才有；单序列数据集返回 nil，事件保持原样，按标的字段消费。
// 失败口径与 periodMarkerSubjects 一致：没有成功提交的序列即失败。
func periodMarkerSeries(exp DatasetPeriodExpectation, success []byte) (universe, failed []*storageeventpb.SeriesRef) {
	tags := make(map[string]struct{}, 2)
	for _, series := range exp.SeriesSnapshot {
		tag := strings.TrimSpace(series.SeriesTag)
		if tag == "" {
			return nil, nil // 序列名单要求每条序列都有 series_tag
		}
		tags[tag] = struct{}{}
	}
	if len(tags) < 2 {
		return nil, nil
	}
	for index, series := range exp.SeriesSnapshot {
		subjectID := strings.TrimSpace(series.SubjectID)
		if subjectID == "" {
			continue
		}
		ref := &storageeventpb.SeriesRef{SubjectId: subjectID, SeriesTag: strings.TrimSpace(series.SeriesTag)}
		universe = append(universe, ref)
		if !bitmapBitSet(success, uint32(index)) {
			failed = append(failed, ref)
		}
	}
	return universe, failed
}

func bitmapSize(count uint32) int { return int((count + 7) / 8) }
func setBitmapBit(bitmap []byte, index uint32) {
	bitmap[index/8] |= byte(1 << (index % 8))
}
func bitmapBitSet(bitmap []byte, index uint32) bool {
	if int(index/8) >= len(bitmap) {
		return false
	}
	return bitmap[index/8]&byte(1<<(index%8)) != 0
}
func bitmapAllSet(bitmap []byte, count uint32) bool {
	for index := uint32(0); index < count; index++ {
		if !bitmapBitSet(bitmap, index) {
			return false
		}
	}
	return true
}
