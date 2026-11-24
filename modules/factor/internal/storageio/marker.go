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
		SpaceId: marker.SpaceID, Marker: protoMarker,
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

// Universe 是周期 marker 里冻结的期望全集（标的）。
type Universe struct {
	Subjects []string
}

// ComputedUniverse 返回周期 marker 里冻结的全集（universe_subject_ids）；marker 不存在时 found 为 false。
// 截面因子的恢复用它：按目标周期冻结的全集重算，不用当前标的池，也不把单个标的当作完整截面。
func (c *Client) ComputedUniverse(ctx context.Context, spaceID, datasetID, triggerEventID string, periodTime int64) (Universe, bool, error) {
	rsp, err := c.getComputed(ctx, spaceID, datasetID, triggerEventID, periodTime)
	if err != nil || rsp == nil || !rsp.GetFound() {
		return Universe{}, false, err
	}
	marker := rsp.GetMarker()
	return Universe{Subjects: append([]string(nil), marker.GetUniverseSubjectIds()...)}, true, nil
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
		SpaceId: spaceID, DatasetId: datasetID, CauseEventId: triggerEventID, PeriodTime: periodTime,
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
// 明细里每个成员的失败标的只记周期级 failed_subjects 之外多出来的部分（相减只在这里做一次）；
// 读取方用 packages/factorname.MemberFailedSubjects 还原完整名单。
func markerToProto(marker PeriodMarker) (*storagepb.DatasetPeriodCompletedMarker, error) {
	universe, failed := normalizedSorted(marker.UniverseSubjects), normalizedSorted(marker.FailedSubjects)
	periodFailed := make(map[string]struct{}, len(failed))
	for _, subject := range failed {
		periodFailed[subject] = struct{}{}
	}
	factors := append([]FactorState(nil), marker.Factors...)
	sort.Slice(factors, func(i, j int) bool { return factors[i].MemberKey < factors[j].MemberKey })
	detailFactors := make([]*factorpb.FactorPeriodState, 0, len(factors))
	for _, factor := range factors {
		var extra []string
		for _, subject := range normalizedSorted(factor.FailedSubjects) {
			if _, shared := periodFailed[subject]; !shared {
				extra = append(extra, subject)
			}
		}
		detailFactors = append(detailFactors, &factorpb.FactorPeriodState{
			FactorId: factor.FactorID, MemberKey: factor.MemberKey, MemberKind: factor.MemberKind, Status: factor.Status,
			FailedSubjects: extra, SpecHash: factor.SourceHash, DefinitionHash: factor.DefinitionHash,
		})
	}
	detail := &factorpb.PeriodDetail{SourceDatasetId: marker.SourceDatasetID, Factors: detailFactors}
	if err := ValidatePeriodDetail(detail, universe, failed, marker.Status); err != nil {
		return nil, err
	}
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(detail)
	if err != nil {
		return nil, fmt.Errorf("编码因子周期明细: %w", err)
	}
	out := &storagepb.DatasetPeriodCompletedMarker{
		DatasetId: marker.ResultDatasetID, Frequency: marker.Frequency, PeriodTime: marker.PeriodTime, Status: marker.Status,
		UniverseSubjectIds: universe, FailedSubjects: failed,
		CauseEventId: marker.TriggerEventID, CompletedAt: timestamppb.New(marker.ComputedAt.UTC()),
		DetailType: PeriodDetailType, Detail: raw,
	}
	if size := proto.Size(out); size > MaxMarkerBytes {
		return nil, fmt.Errorf("因子周期标记体积 %d 字节超过上限 %d 字节（成员 %d 个，期望标的 %d 个，失败标的 %d 个）：Storage 会拒绝这个超大的事件，请减少任务的成员数或标的数",
			size, MaxMarkerBytes, len(detailFactors), len(universe), len(failed))
	}
	return out, nil
}

// MaxMarkerBytes 是因子周期标记（连同明细）允许的体积上限。Storage 的事件上限默认 8 MiB（事件总线的 max_payload_bytes），
// 周期完成事件另有信封与按标的的名单，这里留出余量，在写入前就给出清晰的错误，不等 Storage 拒绝。
const MaxMarkerBytes = 6 << 20

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
