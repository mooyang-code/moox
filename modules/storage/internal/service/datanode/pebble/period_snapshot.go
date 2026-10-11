package pebble

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	cpebble "github.com/cockroachdb/pebble"
	"github.com/mooyang-code/moox/modules/storage/internal/rowidentity"
	frequencypkg "github.com/mooyang-code/moox/packages/frequency"
	"github.com/mooyang-code/moox/packages/seriesset"
)

const (
	seriesSnapshotPrefix  = "__series_snapshot/"
	seriesEffectivePrefix = "__series_effective/"
	// defaultAllowedLatenessSeconds 在数据集未设置 allowed_lateness_seconds 属性时使用。
	defaultAllowedLatenessSeconds = 60
	maxAllowedLatenessSeconds     = 24 * 60 * 60
)

// RegisterSnapshotResult 是登记快照的结果。
type RegisterSnapshotResult struct {
	SeriesHash        string
	ExpectedCount     uint32
	AlreadyRegistered bool
}

// SeriesSnapshotMissingError 表示提交指向的 hash 没有已登记的快照，或生效表中没有可用于该周期的条目。
type SeriesSnapshotMissingError struct{ Reason string }

func (e SeriesSnapshotMissingError) Error() string { return e.Reason }

// PeriodHashMismatchError 表示提交带来的 hash 与生效表规定的该周期 hash 不一致。
type PeriodHashMismatchError struct{ Want, Got string }

func (e PeriodHashMismatchError) Error() string {
	return fmt.Sprintf("series hash %s does not match the effective snapshot %s for this period", e.Got, e.Want)
}

// PeriodDeadlinePassedError 表示周期尚未建立且截止时间已过，迟到的数据交给补采。
type PeriodDeadlinePassedError struct{ Deadline int64 }

func (e PeriodDeadlinePassedError) Error() string {
	return fmt.Sprintf("dataset period deadline %d has passed before the period was created", e.Deadline)
}

// StaleEffectiveFromError 表示生效周期 P 已不在未来（或其后已有周期），且该 P 尚未登记；
// 调用方（Collector）可据此重新选择 P。错误文本以 effective_from_stale 开头，供客户端识别。
type StaleEffectiveFromError struct{ Reason string }

func (e StaleEffectiveFromError) Error() string { return "effective_from_stale: " + e.Reason }

func snapshotScope(spaceID, datasetID, frequency string) string {
	return hex.EncodeToString([]byte(spaceID)) + "/" + hex.EncodeToString([]byte(datasetID)) + "/" + hex.EncodeToString([]byte(frequency)) + "/"
}

func seriesSnapshotKey(spaceID, datasetID, frequency, hash string) []byte {
	return []byte(seriesSnapshotPrefix + snapshotScope(spaceID, datasetID, frequency) + hash)
}

func seriesEffectivePrefixFor(spaceID, datasetID, frequency string) string {
	return seriesEffectivePrefix + snapshotScope(spaceID, datasetID, frequency)
}

func seriesEffectiveKey(spaceID, datasetID, frequency string, from int64) []byte {
	return []byte(seriesEffectivePrefixFor(spaceID, datasetID, frequency) + fmt.Sprintf("%020d", from))
}

func periodAligned(frequency frequencypkg.Frequency, at int64) bool {
	step := int64(frequency.Duration() / time.Second)
	if step <= 0 || step >= 24*60*60 {
		return at > 0
	}
	return at > 0 && at%step == 0
}

// RegisterSeriesSnapshot 保存完整标的列表，并在生效表追加“从 effectiveFrom 起使用该快照”。
// 与周期提交共用 periodMu，检查与追加在同一把锁内完成。
func (s *Store) RegisterSeriesSnapshot(ctx context.Context, spaceID, datasetID, frequency string, series []seriesset.Series, effectiveFrom int64) (RegisterSnapshotResult, error) {
	spaceID, datasetID, frequency = strings.TrimSpace(spaceID), strings.TrimSpace(datasetID), strings.TrimSpace(frequency)
	if spaceID == "" || datasetID == "" {
		return RegisterSnapshotResult{}, invalid("space_id and dataset_id are required")
	}
	parsed, err := frequencypkg.Parse(frequency)
	if err != nil || string(parsed) != frequency {
		return RegisterSnapshotResult{}, invalid("frequency must be canonical")
	}
	if !periodAligned(parsed, effectiveFrom) {
		return RegisterSnapshotResult{}, invalid("effective_from must be positive and aligned to the frequency boundary")
	}
	hash, err := seriesset.VerifySorted(series)
	if err != nil {
		return RegisterSnapshotResult{}, invalidf("series snapshot is invalid: %v", err)
	}
	snapshot := make([]DatasetPeriodSeries, len(series))
	for i, item := range series {
		if err := rowidentity.ValidateSeriesTag(item.SeriesTag); err != nil {
			return RegisterSnapshotResult{}, invalidf("series snapshot series_tag is invalid: %v", err)
		}
		snapshot[i] = DatasetPeriodSeries{SeriesIndex: uint32(i), SubjectID: strings.TrimSpace(item.SubjectID), SeriesTag: item.SeriesTag}
	}
	exp := DatasetPeriodExpectation{SpaceID: spaceID, DatasetID: datasetID, Frequency: frequency, PeriodTime: effectiveFrom, SeriesHash: hash, ExpectedCount: uint32(len(series)), SeriesSnapshot: snapshot}
	if err := s.validatePeriodUniverseSize(exp); err != nil {
		return RegisterSnapshotResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return RegisterSnapshotResult{}, err
	}
	s.datasetWriteMu.RLock()
	defer s.datasetWriteMu.RUnlock()
	deleted, err := s.isDatasetDeleted(spaceID, datasetID)
	if err != nil {
		return RegisterSnapshotResult{}, err
	}
	if deleted {
		return RegisterSnapshotResult{}, ErrDatasetDeleted
	}
	s.periodMu.Lock()
	defer s.periodMu.Unlock()
	result := RegisterSnapshotResult{SeriesHash: hash, ExpectedCount: uint32(len(series))}
	// 同 P 的重放：同 hash 直接成功（即使 P 已过），不同 hash 拒绝。
	if existing, found, err := s.readEffectiveHash(spaceID, datasetID, frequency, effectiveFrom); err != nil {
		return RegisterSnapshotResult{}, err
	} else if found {
		if existing != hash {
			return RegisterSnapshotResult{}, PeriodConflictError{SpaceID: spaceID, DatasetID: datasetID, Frequency: frequency, PeriodTime: effectiveFrom}
		}
		result.AlreadyRegistered = true
		return result, nil
	}
	lastFrom, _, hasLast, err := s.lastEffective(spaceID, datasetID, frequency)
	if err != nil {
		return RegisterSnapshotResult{}, err
	}
	if hasLast && effectiveFrom <= lastFrom {
		return RegisterSnapshotResult{}, StaleEffectiveFromError{Reason: "effective_from must be strictly later than the last registered effective period"}
	}
	if effectiveFrom <= s.periodNow().UTC().Unix() && hasLast {
		return RegisterSnapshotResult{}, StaleEffectiveFromError{Reason: "effective_from must be in the future"}
	}
	// P 之后不能已有周期：已建立的周期不可更换快照。
	if has, err := s.hasPeriodAtOrAfter(spaceID, datasetID, frequency, effectiveFrom); err != nil {
		return RegisterSnapshotResult{}, err
	} else if has {
		return RegisterSnapshotResult{}, StaleEffectiveFromError{Reason: "a dataset period at or after effective_from already exists"}
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return RegisterSnapshotResult{}, err
	}
	batch := s.db.NewBatch()
	defer batch.Close()
	if err := batch.Set(seriesSnapshotKey(spaceID, datasetID, frequency, hash), raw, s.writeOptions); err != nil {
		return RegisterSnapshotResult{}, err
	}
	if err := batch.Set(seriesEffectiveKey(spaceID, datasetID, frequency, effectiveFrom), []byte(hash), s.writeOptions); err != nil {
		return RegisterSnapshotResult{}, err
	}
	if err := batch.Commit(s.writeOptions); err != nil {
		return RegisterSnapshotResult{}, err
	}
	return result, nil
}

func (s *Store) readEffectiveHash(spaceID, datasetID, frequency string, from int64) (string, bool, error) {
	value, err := s.readPeriodValue(seriesEffectiveKey(spaceID, datasetID, frequency, from))
	if errors.Is(err, cpebble.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return string(value), true, nil
}

// lastEffective 返回该数据集该频率生效表中起点最大的一行。
func (s *Store) lastEffective(spaceID, datasetID, frequency string) (int64, string, bool, error) {
	prefix := seriesEffectivePrefixFor(spaceID, datasetID, frequency)
	iter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: []byte(prefix), UpperBound: prefixUpperBound(prefix)})
	if err != nil {
		return 0, "", false, err
	}
	defer iter.Close()
	if !iter.Last() {
		return 0, "", false, iter.Error()
	}
	var from int64
	if _, err := fmt.Sscanf(strings.TrimPrefix(string(iter.Key()), prefix), "%d", &from); err != nil {
		return 0, "", false, err
	}
	return from, string(iter.Value()), true, nil
}

// effectiveHashFor 返回周期时间 period 应使用的 hash：起点 ≤ period 的最后一行。
func (s *Store) effectiveHashFor(spaceID, datasetID, frequency string, period int64) (string, bool, error) {
	prefix := seriesEffectivePrefixFor(spaceID, datasetID, frequency)
	iter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: []byte(prefix), UpperBound: prefixUpperBound(prefix)})
	if err != nil {
		return "", false, err
	}
	defer iter.Close()
	if !iter.SeekLT([]byte(fmt.Sprintf("%s%020d", prefix, period+1))) {
		return "", false, iter.Error()
	}
	return string(iter.Value()), true, nil
}

func prefixUpperBound(prefix string) []byte {
	upper := []byte(prefix)
	for i := len(upper) - 1; i >= 0; i-- {
		upper[i]++
		if upper[i] != 0 {
			return upper[:i+1]
		}
	}
	return nil
}

// hasPeriodAtOrAfter 判断该数据集该频率在 from 及之后是否已有周期记录。
func (s *Store) hasPeriodAtOrAfter(spaceID, datasetID, frequency string, from int64) (bool, error) {
	prefix := periodProgressPrefix + hex.EncodeToString([]byte(spaceID)) + "/" + hex.EncodeToString([]byte(datasetID)) + "/" + hex.EncodeToString([]byte(frequency)) + "/"
	iter, err := s.db.NewIter(&cpebble.IterOptions{LowerBound: []byte(fmt.Sprintf("%s%020d/", prefix, from)), UpperBound: prefixUpperBound(prefix)})
	if err != nil {
		return false, err
	}
	defer iter.Close()
	if !iter.First() {
		return false, iter.Error()
	}
	return true, nil
}

func (s *Store) loadSeriesSnapshot(spaceID, datasetID, frequency, hash string) ([]DatasetPeriodSeries, error) {
	raw, err := s.readPeriodValue(seriesSnapshotKey(spaceID, datasetID, frequency, hash))
	if errors.Is(err, cpebble.ErrNotFound) {
		return nil, SeriesSnapshotMissingError{Reason: "series snapshot is not registered for this hash"}
	}
	if err != nil {
		return nil, err
	}
	var snapshot []DatasetPeriodSeries
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, fmt.Errorf("series snapshot is corrupted: %w", err)
	}
	return snapshot, nil
}

// planLazyPeriod 为尚不存在的周期构造期望：核对生效表与 hash，加载快照，计算截止时间。
// 调用方持有 datasetWriteMu 与 periodMu。
func (s *Store) planLazyPeriod(exp DatasetPeriodExpectation, allowedLatenessSeconds int64, now time.Time) (DatasetPeriodExpectation, error) {
	frequency, err := frequencypkg.Parse(exp.Frequency)
	if err != nil {
		return exp, invalidf("frequency is invalid: %v", err)
	}
	if allowedLatenessSeconds <= 0 {
		allowedLatenessSeconds = defaultAllowedLatenessSeconds
	}
	if allowedLatenessSeconds > maxAllowedLatenessSeconds {
		return exp, invalid("allowed_lateness_seconds exceeds the maximum")
	}
	want, found, err := s.effectiveHashFor(exp.SpaceID, exp.DatasetID, exp.Frequency, exp.PeriodTime)
	if err != nil {
		return exp, err
	}
	if !found {
		return exp, SeriesSnapshotMissingError{Reason: "no series snapshot is effective for this period"}
	}
	if want != exp.SeriesHash {
		return exp, PeriodHashMismatchError{Want: want, Got: exp.SeriesHash}
	}
	snapshot, err := s.loadSeriesSnapshot(exp.SpaceID, exp.DatasetID, exp.Frequency, want)
	if err != nil {
		return exp, err
	}
	if uint32(len(snapshot)) != exp.ExpectedCount {
		return exp, invalid("expected_count does not match the registered series snapshot")
	}
	deadline := frequency.BarEnd(time.Unix(exp.PeriodTime, 0).UTC()).Add(time.Duration(allowedLatenessSeconds) * time.Second).Unix()
	if now.Unix() >= deadline {
		return exp, PeriodDeadlinePassedError{Deadline: deadline}
	}
	exp.DeadlineAt = deadline
	exp.SeriesSnapshot = snapshot
	return exp, nil
}

// PeriodMissingIndexes 返回期望集合减去成功位图后的序号。
func PeriodMissingIndexes(expected uint32, success []byte) []uint32 {
	missing := make([]uint32, 0)
	for index := uint32(0); index < expected; index++ {
		if !bitmapBitSet(success, index) {
			missing = append(missing, index)
		}
	}
	return missing
}

// PeriodStatusByKey 只按周期键查询，不要求调用方提供 hash；周期不存在时 found=false。
func (s *Store) PeriodStatusByKey(ctx context.Context, spaceID, datasetID, frequency string, periodTime int64, includeProgress bool) (*DatasetPeriodProgress, bool, error) {
	if strings.TrimSpace(spaceID) == "" || strings.TrimSpace(datasetID) == "" || strings.TrimSpace(frequency) == "" || periodTime <= 0 {
		return nil, false, invalid("space_id, dataset_id, frequency and period_time are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	s.datasetWriteMu.RLock()
	defer s.datasetWriteMu.RUnlock()
	s.periodMu.Lock()
	defer s.periodMu.Unlock()
	base := periodBase(spaceID, datasetID, frequency, periodTime)
	status, found, err := s.readPeriodStatus(base)
	if err != nil || !found {
		return nil, false, err
	}
	current, err := s.readPeriodExpectation(base)
	if err != nil {
		return nil, false, err
	}
	progress := &DatasetPeriodProgress{DatasetPeriodExpectation: current, Status: status}
	if includeProgress {
		bitmap, err := s.readPeriodBitmap(base)
		if err != nil {
			return nil, false, err
		}
		progress.Bitmap = bitmap
	}
	return progress, true, nil
}
