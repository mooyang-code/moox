package stream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
)

// StreamCollectorService 是 Collector 上供 collector-stream 调用的服务。
const StreamCollectorService = "trpc.moox.collector.StreamCollector"

// PlanVersion 是计划记录 ID 与阶段（transition、final）。
type PlanVersion struct {
	ID    int64
	Stage string
}

// PlanClient 是 StreamCollector 的客户端：心跳、拉计划、上报周期统计。
type PlanClient struct {
	Gateway    gatewayclient.Invoker
	SpaceID    string
	InstanceID string
	Version    string
}

func (c *PlanClient) call(ctx context.Context, method string, request, response any) error {
	if c == nil || c.Gateway == nil {
		return errors.New("stream plan client requires the gateway client")
	}
	metadata := gatewayclient.CallMetadataFromContext(ctx)
	metadata.SpaceID = c.SpaceID
	return c.Gateway.Invoke(gatewayclient.WithCallMetadata(ctx, metadata), StreamCollectorService, method, request, response)
}

func retError(method string, info *pb.RetInfo) error {
	if info.GetCode() != pb.ErrorCode_SUCCESS {
		return fmt.Errorf("%s: %s", method, info.GetMsg())
	}
	return nil
}

// Heartbeat 上报已应用的计划版本与连接状态，返回 Collector 要求的最新版本。
func (c *PlanClient) Heartbeat(ctx context.Context, applied PlanVersion, markets []MarketStatus) (PlanVersion, error) {
	request := &pb.StreamHeartbeatReq{SpaceId: c.SpaceID, InstanceId: c.InstanceID, AppliedPlanId: applied.ID, AppliedStage: applied.Stage, SentAtMs: time.Now().UnixMilli(), Version: c.Version}
	for _, market := range markets {
		for _, conn := range market.Conns {
			status := &pb.StreamConnStatus{Market: market.Market, Frequency: conn.Group, Streams: int32(conn.Streams), Reconnects: conn.Reconnects, Subscribed: conn.Subscribed, Endpoint: conn.Endpoint}
			if !conn.LastMessage.IsZero() {
				status.LastMessageAt = conn.LastMessage.UTC().Format(time.RFC3339Nano)
			}
			request.Conns = append(request.Conns, status)
		}
	}
	var response pb.StreamHeartbeatRsp
	if err := c.call(ctx, "StreamHeartbeat", request, &response); err != nil {
		return PlanVersion{}, err
	}
	if err := retError("StreamHeartbeat", response.GetRetInfo()); err != nil {
		return PlanVersion{}, err
	}
	return PlanVersion{ID: response.GetPlanId(), Stage: response.GetStage()}, nil
}

// Pull 拉取本空间分给推送与本地执行端的计划项。
func (c *PlanClient) Pull(ctx context.Context) (PlanVersion, []PlanItem, time.Duration, error) {
	var response pb.PullStreamPlanRsp
	if err := c.call(ctx, "PullStreamPlan", &pb.PullStreamPlanReq{SpaceId: c.SpaceID}, &response); err != nil {
		return PlanVersion{}, nil, 0, err
	}
	if err := retError("PullStreamPlan", response.GetRetInfo()); err != nil {
		return PlanVersion{}, nil, 0, err
	}
	items := make([]PlanItem, 0, len(response.GetItems()))
	for _, item := range response.GetItems() {
		var plan marketfetch.Plan
		decoder := json.NewDecoder(strings.NewReader(item.GetPlanJson()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&plan); err != nil {
			return PlanVersion{}, nil, 0, fmt.Errorf("decode plan %s: %w", item.GetName(), err)
		}
		if err := plan.Validate(); err != nil {
			return PlanVersion{}, nil, 0, fmt.Errorf("plan %s: %w", item.GetName(), err)
		}
		items = append(items, PlanItem{Name: item.GetName(), Executor: item.GetExecutor(), Market: item.GetMarket(), Plan: &plan})
	}
	return PlanVersion{ID: response.GetPlanId(), Stage: response.GetStage()}, items, time.Duration(response.GetCommitReserveMs()) * time.Millisecond, nil
}

// Report 批量上报周期统计与疑似下架的标的。
func (c *PlanClient) Report(ctx context.Context, stats []PeriodStat, suspects []Suspect) error {
	request := &pb.ReportStreamPeriodsReq{SpaceId: c.SpaceID, InstanceId: c.InstanceID}
	for _, stat := range stats {
		request.Periods = append(request.Periods, PeriodStatProto(stat))
	}
	for _, suspect := range suspects {
		request.Suspects = append(request.Suspects, &pb.StreamSuspect{Market: suspect.Market, SubjectId: suspect.SubjectID, Reason: suspect.Reason})
	}
	var response pb.ReportStreamPeriodsRsp
	if err := c.call(ctx, "ReportStreamPeriods", request, &response); err != nil {
		return err
	}
	return retError("ReportStreamPeriods", response.GetRetInfo())
}

// PeriodStatProto 把统计转成协议消息。
func PeriodStatProto(stat PeriodStat) *pb.StreamPeriodStat {
	return &pb.StreamPeriodStat{
		Market: stat.Market, DatasetId: stat.DatasetID, Frequency: stat.Frequency, Period: stat.Period.Unix(),
		Expected: int32(stat.Expected), Arrived: int32(stat.Arrived), Missing: int32(stat.Missing), LateAfterCommit: int32(stat.LateAfterCommit),
		LastArrivalMs: stat.LastArrival.Milliseconds(), CommitDoneMs: stat.CommitDone.Milliseconds(), CommitMs: stat.CommitDuration.Milliseconds(),
		Committed: stat.Committed, Error: stat.Err, Suspected: int32(stat.Suspected),
	}
}
