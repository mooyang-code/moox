package storageio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/seriesset"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (c *Client) ReportComputed(ctx context.Context, marker PeriodMarker) error {
	if err := c.primaryReady("report factor period computed"); err != nil {
		return err
	}
	if strings.TrimSpace(marker.SpaceID) == "" || strings.TrimSpace(marker.ResultDatasetID) == "" ||
		strings.TrimSpace(marker.SourceDatasetID) == "" || strings.TrimSpace(marker.Frequency) == "" ||
		strings.TrimSpace(marker.TriggerEventID) == "" || marker.PeriodTime <= 0 {
		return errors.New("space, result/source dataset, frequency, positive period_time and trigger_event_id are required")
	}
	protoMarker, err := markerToProto(marker)
	if err != nil {
		return err
	}
	rsp, err := c.primary.ReportDatasetPeriodCompleted(ctx, &storagepb.ReportDatasetPeriodCompletedReq{
		AuthInfo: c.auth, SpaceId: marker.SpaceID, Marker: protoMarker,
	})
	if err != nil {
		return rpcError("report factor period computed", err)
	}
	if rsp == nil {
		return fmt.Errorf("%w: report factor period computed returned an empty response", ErrInfra)
	}
	if err := responseError("report factor period computed", rsp.GetRetInfo()); err != nil {
		if rsp.GetRetInfo().GetCode() == commonpb.ErrorCode_CONFLICT {
			// A concurrent redelivery already reported this period; the next
			// attempt's ComputedExists pre-check settles it.
			return fmt.Errorf("%w: %w", ErrInfra, err)
		}
		return err
	}
	expected := computedEventID(marker.SpaceID, protoMarker)
	if rsp.GetEventId() != expected {
		return fmt.Errorf("reported factor period event_id %q does not match deterministic ID %q", rsp.GetEventId(), expected)
	}
	return nil
}

func (c *Client) ComputedExists(ctx context.Context, spaceID, datasetID, triggerEventID string, periodTime int64) (bool, error) {
	rsp, err := c.getComputed(ctx, spaceID, datasetID, triggerEventID, periodTime)
	if err != nil {
		return false, err
	}
	return rsp != nil && rsp.GetFound(), nil
}

// Universe 是周期 marker 里冻结的期望全集：Subjects 按标的；Series 按序列（标的、series_tag），只有统一数据集才有。
type Universe struct {
	Subjects []string
	Series   []seriesset.Series
}

// ComputedUniverse 返回周期 marker 里冻结的全集（universe_subject_ids 与 universe_series）；marker 不存在时 found 为 false。
// 截面因子的恢复用它：按目标周期冻结的全集重算，不用当前标的池，也不把单个标的当作完整截面；统一数据集按序列判断全集。
func (c *Client) ComputedUniverse(ctx context.Context, spaceID, datasetID, triggerEventID string, periodTime int64) (Universe, bool, error) {
	rsp, err := c.getComputed(ctx, spaceID, datasetID, triggerEventID, periodTime)
	if err != nil || rsp == nil || !rsp.GetFound() {
		return Universe{}, false, err
	}
	marker := rsp.GetMarker()
	return Universe{
		Subjects: append([]string(nil), marker.GetUniverseSubjectIds()...),
		Series:   seriesFromMarker(marker.GetUniverseSeries()),
	}, true, nil
}

// getComputed 读取周期 marker；NOT_FOUND 返回 (nil, nil)。
func (c *Client) getComputed(ctx context.Context, spaceID, datasetID, triggerEventID string, periodTime int64) (*storagepb.GetDatasetPeriodCompletedRsp, error) {
	if err := c.primaryReady("get factor period computed"); err != nil {
		return nil, err
	}
	if strings.TrimSpace(spaceID) == "" || strings.TrimSpace(datasetID) == "" || strings.TrimSpace(triggerEventID) == "" || periodTime <= 0 {
		return nil, errors.New("space_id, dataset_id, trigger_event_id and positive period_time are required")
	}
	rsp, err := c.primary.GetDatasetPeriodCompleted(ctx, &storagepb.GetDatasetPeriodCompletedReq{
		AuthInfo: c.auth, SpaceId: spaceID, DatasetId: datasetID, CauseEventId: triggerEventID, PeriodTime: periodTime,
	})
	if err != nil {
		return nil, rpcError("get factor period computed", err)
	}
	if rsp == nil {
		return nil, fmt.Errorf("%w: get factor period computed returned an empty response", ErrInfra)
	}
	ret := rsp.GetRetInfo()
	if ret != nil && ret.GetCode() == commonpb.ErrorCode_NOT_FOUND {
		return nil, nil
	}
	if err := responseError("get factor period computed", ret); err != nil {
		return nil, err
	}
	return rsp, nil
}

// PeriodDetailType 是因子周期明细放进 DatasetPeriodCompleted.detail 时的 detail_type。
const PeriodDetailType = "moox.factor.PeriodDetail.v1"

// markerToProto 把因子这一期的结果构造成写入方周期完成报告：每个因子成员的状态编码成 PeriodDetail 放进 detail，
// Storage 与 View 只原样透传，不解析。
func markerToProto(marker PeriodMarker) (*storagepb.DatasetPeriodCompletedMarker, error) {
	factors := append([]FactorState(nil), marker.Factors...)
	sort.Slice(factors, func(i, j int) bool { return factors[i].MemberKey < factors[j].MemberKey })
	detailFactors := make([]*factorpb.FactorPeriodState, 0, len(factors))
	for _, factor := range factors {
		detailFactors = append(detailFactors, &factorpb.FactorPeriodState{
			FactorId: factor.FactorID, MemberKey: factor.MemberKey, MemberKind: factor.MemberKind, Status: factor.Status,
			FailedSubjects: normalizedSorted(factor.FailedSubjects), SpecHash: factor.SourceHash,
			DefinitionHash: factor.DefinitionHash, GapSubjects: normalizedSorted(factor.GapSubjects),
			FailedSeries: seriesToFactorPB(factor.FailedSeries), GapSeries: seriesToFactorPB(factor.GapSeries),
			SkippedSeriesTags: normalizedSorted(factor.SkippedSeriesTags),
		})
	}
	detail := &factorpb.PeriodDetail{SourceDatasetId: marker.SourceDatasetID, Factors: detailFactors}
	detail.SharedFailedSeries = shareFailedSeries(detailFactors)
	universe, failed := normalizedSorted(marker.UniverseSubjects), normalizedSorted(marker.FailedSubjects)
	universeSeries, failedSeries := normalizedSeries(marker.UniverseSeries), normalizedSeries(marker.FailedSeries)
	if err := ValidatePeriodDetail(detail, universe, universeSeries, marker.Status); err != nil {
		return nil, err
	}
	// 周期级失败序列的标的必须在失败标的里（有一条序列失败的标的就是失败标的）：Storage 发布事件时按同一条规则校验，
	// 这里先查，违反时给出中文错误，而不是等 Storage 拒发。
	failedSet := make(map[string]struct{}, len(failed))
	for _, subject := range failed {
		failedSet[subject] = struct{}{}
	}
	for _, item := range failedSeries {
		if _, ok := failedSet[item.SubjectID]; !ok {
			return nil, fmt.Errorf("因子周期标记的失败序列 %s/%s 的标的不在失败标的里", item.SubjectID, item.SeriesTag)
		}
	}
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(detail)
	if err != nil {
		return nil, fmt.Errorf("编码因子周期明细: %w", err)
	}
	out := &storagepb.DatasetPeriodCompletedMarker{
		DatasetId: marker.ResultDatasetID, Frequency: marker.Frequency, PeriodTime: marker.PeriodTime, Status: marker.Status,
		UniverseSubjectIds: universe, FailedSubjects: failed,
		UniverseSeries: seriesToMarker(universeSeries), FailedSeries: seriesToMarker(failedSeries),
		CauseEventId: marker.TriggerEventID, CompletedAt: timestamppb.New(marker.ComputedAt.UTC()),
		DetailType: PeriodDetailType, Detail: raw,
	}
	if size := proto.Size(out); size > MaxMarkerBytes {
		return nil, fmt.Errorf("因子周期标记体积 %d 字节超过上限 %d 字节（成员 %d 个，期望标的 %d 个，期望序列 %d 条，失败序列 %d 条）：Storage 会拒绝这个超大的事件，请减少任务的成员数或标的数",
			size, MaxMarkerBytes, len(detailFactors), len(universe), len(universeSeries), len(out.GetFailedSeries()))
	}
	return out, nil
}

// MaxMarkerBytes 是因子周期标记（连同明细）允许的体积上限。Storage 的事件上限默认 8 MiB（事件总线的 max_payload_bytes），
// 周期完成事件另有信封与按标的、按序列的名单，这里留出余量，在写入前就给出清晰的错误，不等 Storage 拒绝。
const MaxMarkerBytes = 6 << 20

// shareFailedSeries 取所有成员失败序列的交集，从各成员的名单里去掉，作为周期级的一份返回（见 PeriodDetail.shared_failed_series）。
// 永续整体晚到时每个成员的失败序列相同，不抽出来的话 230 个成员 × 数百序列会让标记超过事件体积上限。没有交集时返回 nil。
func shareFailedSeries(factors []*factorpb.FactorPeriodState) []*factorpb.SeriesRef {
	if len(factors) == 0 {
		return nil
	}
	type key struct{ subject, tag string }
	common := make(map[key]*factorpb.SeriesRef, len(factors[0].GetFailedSeries()))
	for _, ref := range factors[0].GetFailedSeries() {
		common[key{ref.GetSubjectId(), ref.GetSeriesTag()}] = ref
	}
	for _, state := range factors[1:] {
		have := make(map[key]struct{}, len(state.GetFailedSeries()))
		for _, ref := range state.GetFailedSeries() {
			have[key{ref.GetSubjectId(), ref.GetSeriesTag()}] = struct{}{}
		}
		for k := range common {
			if _, ok := have[k]; !ok {
				delete(common, k)
			}
		}
	}
	if len(common) == 0 {
		return nil
	}
	shared := make([]*factorpb.SeriesRef, 0, len(common))
	for _, ref := range common {
		shared = append(shared, ref)
	}
	sort.Slice(shared, func(i, j int) bool {
		if shared[i].GetSubjectId() != shared[j].GetSubjectId() {
			return shared[i].GetSubjectId() < shared[j].GetSubjectId()
		}
		return shared[i].GetSeriesTag() < shared[j].GetSeriesTag()
	})
	for _, state := range factors {
		var rest []*factorpb.SeriesRef
		for _, ref := range state.GetFailedSeries() {
			if _, ok := common[key{ref.GetSubjectId(), ref.GetSeriesTag()}]; !ok {
				rest = append(rest, ref)
			}
		}
		state.FailedSeries = rest
	}
	return shared
}

// MemberFailedSeries 还原成员完整的失败序列：周期级共享的 shared_failed_series 加上该成员自己的 failed_series（去重、已排序）。
// 合并规则是共享包 packages/seriesset 的 Union（策略等不能引用 factor/internal 的读取方直接用它）。
// 读取 PeriodDetail 的地方一律用它取成员的失败序列，不要直接读 FactorPeriodState.failed_series。
func MemberFailedSeries(detail *factorpb.PeriodDetail, state *factorpb.FactorPeriodState) []seriesset.Series {
	return seriesset.Union(factorSeries(detail.GetSharedFailedSeries()), factorSeries(state.GetFailedSeries()))
}

func factorSeries(refs []*factorpb.SeriesRef) []seriesset.Series {
	out := make([]seriesset.Series, 0, len(refs))
	for _, ref := range refs {
		out = append(out, seriesset.Series{SubjectID: ref.GetSubjectId(), SeriesTag: ref.GetSeriesTag()})
	}
	return out
}

func normalizedSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// computedEventID 是 Storage 为这条写入方周期完成生成的确定性事件 ID（与 Storage 的 producer 标记规则一致）。
func computedEventID(spaceID string, marker *storagepb.DatasetPeriodCompletedMarker) string {
	parts := []string{
		"producer-period", spaceID, marker.GetDatasetId(), marker.GetCauseEventId(), strconv.FormatInt(marker.GetPeriodTime(), 10),
	}
	hash := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "storage-marker-" + hex.EncodeToString(hash[:16])
}

// normalizedSeries 去掉空项与重复项，按（标的、series_tag）排序；没有序列时返回 nil。
func normalizedSeries(values []seriesset.Series) []seriesset.Series { return seriesset.Union(values) }

func seriesToMarker(values []seriesset.Series) []*storagepb.SeriesRef {
	if len(values) == 0 {
		return nil
	}
	out := make([]*storagepb.SeriesRef, len(values))
	for i, value := range values {
		out[i] = &storagepb.SeriesRef{SubjectId: value.SubjectID, SeriesTag: value.SeriesTag}
	}
	return out
}

func seriesFromMarker(values []*storagepb.SeriesRef) []seriesset.Series {
	if len(values) == 0 {
		return nil
	}
	out := make([]seriesset.Series, len(values))
	for i, value := range values {
		out[i] = seriesset.Series{SubjectID: value.GetSubjectId(), SeriesTag: value.GetSeriesTag()}
	}
	return out
}

func seriesToFactorPB(values []seriesset.Series) []*factorpb.SeriesRef {
	values = normalizedSeries(values)
	if len(values) == 0 {
		return nil
	}
	out := make([]*factorpb.SeriesRef, len(values))
	for i, value := range values {
		out[i] = &factorpb.SeriesRef{SubjectId: value.SubjectID, SeriesTag: value.SeriesTag}
	}
	return out
}
