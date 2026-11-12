package hostmetrics

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SampleRetention 是主机快照历史的保留期，由数据清理定时任务按它删除。
const SampleRetention = 7 * 24 * time.Hour

// ForecastHistoryLimit 覆盖 7 天每分钟一行，再留一点边界余量；Doctor 做磁盘预测时一次读完。
const ForecastHistoryLimit = 7*24*60 + 8

// defaultHistoryLimit 是未指定条数时返回的最多行数。
const defaultHistoryLimit = 500

type hostSampleRecord struct {
	ID         int64     `gorm:"column:c_id;primaryKey"`
	AgentID    string    `gorm:"column:c_agent_id"`
	ObservedAt time.Time `gorm:"column:c_observed_at"`
	Snapshot   []byte    `gorm:"column:c_snapshot"`
}

func (hostSampleRecord) TableName() string { return "t_monitor_host_samples" }

// SampleStore 把主机快照按分钟存进 Monitor 自己的库。
type SampleStore struct {
	db *gorm.DB
}

func NewSampleStore(db *gorm.DB) *SampleStore { return &SampleStore{db: db} }

// Write 按（主机，分钟）写入一份快照；同一分钟重复写入时覆盖，重放是幂等的。
func (s *SampleStore) Write(ctx context.Context, agentID string, observedAt time.Time, snapshot *hostmetricpb.HostSnapshot) error {
	if s == nil || s.db == nil {
		return errors.New("主机快照库未初始化")
	}
	if agentID == "" || snapshot == nil {
		return errors.New("主机快照缺少采集代理编号或内容")
	}
	encoded, err := proto.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("编码主机快照: %w", err)
	}
	record := hostSampleRecord{AgentID: agentID, ObservedAt: observedAt.UTC().Truncate(time.Minute), Snapshot: encoded}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "c_agent_id"}, {Name: "c_observed_at"}},
		DoUpdates: clause.AssignmentColumns([]string{"c_snapshot"}),
	}).Create(&record).Error
}

// History 返回 [start, end] 内最新的 limit 份快照，按时间升序。limit 不大于 0 时取默认值，最多 ForecastHistoryLimit。
func (s *SampleStore) History(ctx context.Context, agentID string, start, end time.Time, limit int) ([]HistoryPoint, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("主机快照库未初始化")
	}
	if agentID == "" {
		return nil, errors.New("缺少采集代理编号")
	}
	if limit <= 0 {
		limit = defaultHistoryLimit
	}
	limit = min(limit, ForecastHistoryLimit)
	var records []hostSampleRecord
	if err := s.db.WithContext(ctx).
		Where("c_agent_id = ? AND c_observed_at >= ? AND c_observed_at <= ?", agentID, start.UTC(), end.UTC()).
		Order("c_observed_at DESC").Limit(limit).Find(&records).Error; err != nil {
		return nil, fmt.Errorf("读取主机快照历史: %w", err)
	}
	out := make([]HistoryPoint, len(records))
	for i, record := range records {
		snapshot := new(hostmetricpb.HostSnapshot)
		if err := proto.Unmarshal(record.Snapshot, snapshot); err != nil {
			return nil, fmt.Errorf("解码主机快照 agent_id=%s observed_at=%s: %w", agentID, record.ObservedAt.UTC().Format(time.RFC3339), err)
		}
		// 查询按时间倒序取最新的 limit 行，这里反过来按升序返回。
		out[len(records)-1-i] = HistoryPoint{AgentID: agentID, ObservedAt: record.ObservedAt.UTC().Format(time.RFC3339Nano), Snapshot: snapshot}
	}
	return out, nil
}

// DeleteBefore 删除采样时间早于 cutoff 的快照。
func (s *SampleStore) DeleteBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	return store.DeleteBefore(ctx, s.db, "t_monitor_host_samples", "c_observed_at", cutoff.UTC())
}
