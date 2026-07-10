package hostmetrics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/messagepb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
)

const (
	Topic       = "moox.metrics.host.reported.v1"
	ContentType = "application/x-protobuf; message=trpc.moox.hostagent.HostMetric"
	Stream      = "MOOX_METRICS"
	Durable     = "monitor_hostmetrics_ingest_v1"
	SpaceID     = "moox_system"
	DLQTopic    = "moox.dlq.message.rejected.v1"
)

type Store struct{ db *gorm.DB }

func NewStore(db *gorm.DB) *Store { return &Store{db: db} }
func (s *Store) EnsureSchema() error {
	if s == nil || s.db == nil {
		return errors.New("host metric database is nil")
	}
	return s.db.Exec(`CREATE TABLE IF NOT EXISTS t_monitor_host_agents (c_agent_id TEXT PRIMARY KEY, c_hostname TEXT NOT NULL DEFAULT '', c_boot_id TEXT NOT NULL DEFAULT '', c_first_seen_at DATETIME NOT NULL, c_last_seen_at DATETIME NOT NULL, c_is_archived INTEGER NOT NULL DEFAULT 0); CREATE TABLE IF NOT EXISTS t_monitor_host_inbox (c_message_id TEXT PRIMARY KEY, c_agent_id TEXT NOT NULL, c_stream_sequence INTEGER NOT NULL DEFAULT 0, c_payload_sha256 TEXT NOT NULL, c_received_at DATETIME NOT NULL, c_status TEXT NOT NULL DEFAULT 'projected'); CREATE TABLE IF NOT EXISTS t_monitor_host_latest (c_agent_id TEXT PRIMARY KEY, c_occurred_at DATETIME NOT NULL, c_payload BLOB NOT NULL, c_updated_at DATETIME NOT NULL); CREATE TABLE IF NOT EXISTS t_monitor_host_history (c_id INTEGER PRIMARY KEY AUTOINCREMENT, c_agent_id TEXT NOT NULL, c_observed_at DATETIME NOT NULL, c_payload BLOB NOT NULL); CREATE INDEX IF NOT EXISTS idx_monitor_host_history_agent_time ON t_monitor_host_history(c_agent_id, c_observed_at DESC);`).Error
}

func ValidateMessage(msg *messagepb.MooxMessage) (*hostmetricpb.HostMetric, error) {
	if msg == nil {
		return nil, errors.New("message is nil")
	}
	if msg.GetProtocolVersion() != 1 || msg.GetTopic() != Topic || msg.GetKind() != messagepb.MessageKind_MESSAGE_KIND_SNAPSHOT || msg.GetContentType() != ContentType {
		return nil, errors.New("host metric envelope contract mismatch")
	}
	if msg.GetSpaceId() != SpaceID || msg.GetSequence() != 0 {
		return nil, errors.New("host metric space or sequence mismatch")
	}
	if parsed, err := uuid.Parse(msg.GetMessageId()); err != nil || parsed.Version() != 7 {
		return nil, errors.New("host metric message_id must be UUIDv7")
	}
	if msg.GetOccurredAt() == nil || msg.GetPublishedAt() == nil || !msg.GetOccurredAt().IsValid() || !msg.GetPublishedAt().IsValid() {
		return nil, errors.New("host metric timestamps are invalid")
	}
	now := time.Now().UTC()
	occurred := msg.GetOccurredAt().AsTime()
	if occurred.Before(now.Add(-15*time.Minute)) || occurred.After(now.Add(2*time.Minute)) {
		return nil, errors.New("host metric occurred_at is outside the accepted clock-skew window")
	}
	p := msg.GetProducer()
	if p == nil || p.GetServiceName() != "moox-host-agent" || uuid.Validate(p.GetInstanceId()) != nil || strings.TrimSpace(p.GetNodeId()) == "" {
		return nil, errors.New("host metric producer identity is invalid")
	}
	metric := new(hostmetricpb.HostMetric)
	if err := proto.Unmarshal(msg.GetPayload(), metric); err != nil {
		return nil, fmt.Errorf("decode host metric: %w", err)
	}
	if metric.GetSnapshot() == nil {
		return nil, errors.New("host metric snapshot is missing")
	}
	if err := validateSnapshot(metric.GetSnapshot()); err != nil {
		return nil, err
	}
	return metric, nil
}

func validateSnapshot(s *hostmetricpb.HostSnapshot) error {
	if s.GetCpu() == nil || s.GetMemory() == nil {
		return errors.New("host metric cpu/memory are required")
	}
	if s.GetCpu().GetLogicalCores() == 0 {
		return errors.New("logical_cores must be positive")
	}
	if err := percent(s.GetCpu().GetUsagePercent(), s.GetCpu().GetUsageAvailable()); err != nil {
		return err
	}
	if err := percent(s.GetMemory().GetUsagePercent(), true); err != nil {
		return err
	}
	if err := memoryBounds(s.GetMemory()); err != nil {
		return err
	}
	seen := map[string]struct{}{}
	for _, f := range s.GetFilesystems() {
		if strings.TrimSpace(f.GetMountpoint()) == "" {
			return errors.New("filesystem mountpoint is empty")
		}
		if _, ok := seen["fs:"+f.GetDevice()+"\x00"+f.GetMountpoint()]; ok {
			return errors.New("duplicate filesystem key")
		}
		seen["fs:"+f.GetDevice()+"\x00"+f.GetMountpoint()] = struct{}{}
		for _, value := range []uint64{f.GetTotalBytes(), f.GetUsedBytes(), f.GetAvailableBytes()} {
			if value > math.MaxInt64 {
				return errors.New("filesystem counter exceeds int64")
			}
		}
		if err := percent(f.GetUsagePercent(), true); err != nil {
			return err
		}
	}
	seen = map[string]struct{}{}
	for _, d := range s.GetDisks() {
		if d.GetDevice() == "" {
			return errors.New("disk device is empty")
		}
		if _, ok := seen[d.GetDevice()]; ok {
			return errors.New("duplicate disk key")
		}
		seen[d.GetDevice()] = struct{}{}
		for _, value := range []uint64{d.GetReadBytesTotal(), d.GetWriteBytesTotal(), d.GetReadOpsTotal(), d.GetWriteOpsTotal(), d.GetIoTimeMsTotal()} {
			if value > math.MaxInt64 {
				return errors.New("disk counter exceeds int64")
			}
		}
		if d.GetRateAvailable() {
			for _, value := range []float64{d.GetReadBytesPerSecond(), d.GetWriteBytesPerSecond(), d.GetReadIops(), d.GetWriteIops(), d.GetUtilizationPercent()} {
				if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
					return errors.New("disk rate is invalid")
				}
			}
			if d.GetUtilizationPercent() > 100 {
				return errors.New("disk utilization is invalid")
			}
		}
	}
	seen = map[string]struct{}{}
	for _, n := range s.GetNetworks() {
		if n.GetDevice() == "" {
			return errors.New("network device is empty")
		}
		if _, ok := seen[n.GetDevice()]; ok {
			return errors.New("duplicate network key")
		}
		seen[n.GetDevice()] = struct{}{}
		for _, value := range []uint64{n.GetReceiveBytesTotal(), n.GetTransmitBytesTotal(), n.GetReceiveErrorsTotal(), n.GetTransmitErrorsTotal(), n.GetReceiveDroppedTotal(), n.GetTransmitDroppedTotal()} {
			if value > math.MaxInt64 {
				return errors.New("network counter exceeds int64")
			}
		}
		if n.GetRateAvailable() {
			for _, value := range []float64{n.GetReceiveBytesPerSecond(), n.GetTransmitBytesPerSecond()} {
				if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
					return errors.New("network rate is invalid")
				}
			}
		}
	}
	return nil
}

func memoryBounds(m *hostmetricpb.MemoryMetric) error {
	if m.GetTotalBytes() > math.MaxInt64 || m.GetUsedBytes() > math.MaxInt64 || m.GetAvailableBytes() > math.MaxInt64 {
		return errors.New("memory counter exceeds int64")
	}
	if m.GetUsedBytes() > m.GetTotalBytes() || m.GetAvailableBytes() > m.GetTotalBytes() {
		return errors.New("memory counters exceed total")
	}
	return nil
}
func percent(v float64, available bool) error {
	if !available {
		return nil
	}
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 100 {
		return errors.New("metric percentage is invalid")
	}
	return nil
}

func (s *Store) Ingest(ctx context.Context, d *jetstream.Delivery) error {
	if s == nil || s.db == nil || d == nil {
		return errors.New("host metric store or delivery is nil")
	}
	metric, err := ValidateMessage(d.Message)
	if err != nil {
		return err
	}
	msg := d.Message
	now := time.Now().UTC()
	hash := sha256.Sum256(msg.GetPayload())
	hashText := hex.EncodeToString(hash[:])
	p := msg.GetProducer()
	tx := s.db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return tx.Error
	}
	var inbox struct {
		MessageID string `gorm:"column:c_message_id"`
	}
	queryErr := tx.Raw("SELECT c_message_id FROM t_monitor_host_inbox WHERE c_message_id = ?", msg.GetMessageId()).Scan(&inbox).Error
	if queryErr != nil {
		tx.Rollback()
		return queryErr
	}
	if inbox.MessageID != "" {
		tx.Rollback()
		return d.Ack(ctx)
	}
	if err := tx.Exec("INSERT INTO t_monitor_host_inbox(c_message_id,c_agent_id,c_stream_sequence,c_payload_sha256,c_received_at,c_status) VALUES(?,?,?,?,?,?)", msg.GetMessageId(), p.GetInstanceId(), d.StreamSeq, hashText, now, "projected").Error; err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Exec(`INSERT INTO t_monitor_host_agents(c_agent_id,c_hostname,c_boot_id,c_first_seen_at,c_last_seen_at,c_is_archived) VALUES(?,?,?,?,?,0) ON CONFLICT(c_agent_id) DO UPDATE SET c_hostname=excluded.c_hostname,c_boot_id=excluded.c_boot_id,c_last_seen_at=excluded.c_last_seen_at`, p.GetInstanceId(), p.GetNodeId(), p.GetBootId(), now, now).Error; err != nil {
		tx.Rollback()
		return err
	}
	payload, _ := proto.Marshal(metric)
	if err := tx.Exec(`INSERT INTO t_monitor_host_latest(c_agent_id,c_occurred_at,c_payload,c_updated_at) VALUES(?,?,?,?) ON CONFLICT(c_agent_id) DO UPDATE SET c_occurred_at=excluded.c_occurred_at,c_payload=excluded.c_payload,c_updated_at=excluded.c_updated_at`, p.GetInstanceId(), msg.GetOccurredAt().AsTime(), payload, now).Error; err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Exec(`INSERT INTO t_monitor_host_history(c_agent_id,c_observed_at,c_payload) VALUES(?,?,?)`, p.GetInstanceId(), msg.GetOccurredAt().AsTime(), payload).Error; err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit().Error; err != nil {
		return err
	}
	return d.Ack(ctx)
}

type AgentView struct {
	AgentID, Hostname, BootID, LastSeenAt string
	Archived                              bool
	Snapshot                              *hostmetricpb.HostSnapshot
}

type HistoryPoint struct {
	AgentID, ObservedAt string
	Snapshot            *hostmetricpb.HostSnapshot
}

func (s *Store) ListAgents(ctx context.Context) ([]AgentView, error) {
	var rows []struct {
		AgentID  string    `gorm:"column:c_agent_id"`
		Hostname string    `gorm:"column:c_hostname"`
		BootID   string    `gorm:"column:c_boot_id"`
		LastSeen time.Time `gorm:"column:c_last_seen_at"`
		Archived bool      `gorm:"column:c_is_archived"`
		Payload  []byte    `gorm:"column:c_payload"`
	}
	if err := s.db.WithContext(ctx).Raw(`SELECT a.c_agent_id,a.c_hostname,a.c_boot_id,a.c_last_seen_at,a.c_is_archived,l.c_payload FROM t_monitor_host_agents a LEFT JOIN t_monitor_host_latest l ON l.c_agent_id=a.c_agent_id ORDER BY a.c_hostname,a.c_agent_id`).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]AgentView, 0, len(rows))
	for _, row := range rows {
		view := AgentView{AgentID: row.AgentID, Hostname: row.Hostname, BootID: row.BootID, LastSeenAt: row.LastSeen.UTC().Format(time.RFC3339Nano), Archived: row.Archived}
		if len(row.Payload) > 0 {
			metric := new(hostmetricpb.HostMetric)
			if err := proto.Unmarshal(row.Payload, metric); err != nil {
				return nil, err
			}
			view.Snapshot = metric.GetSnapshot()
		}
		out = append(out, view)
	}
	return out, nil
}

func (s *Store) History(ctx context.Context, agentID string, start, end time.Time, limit int) ([]HistoryPoint, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	var rows []struct {
		AgentID  string    `gorm:"column:c_agent_id"`
		Observed time.Time `gorm:"column:c_observed_at"`
		Payload  []byte    `gorm:"column:c_payload"`
	}
	if err := s.db.WithContext(ctx).Raw(`SELECT c_agent_id,c_observed_at,c_payload FROM t_monitor_host_history WHERE c_agent_id=? AND c_observed_at>=? AND c_observed_at<=? ORDER BY c_observed_at ASC LIMIT ?`, agentID, start, end, limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]HistoryPoint, 0, len(rows))
	for _, row := range rows {
		metric := new(hostmetricpb.HostMetric)
		if err := proto.Unmarshal(row.Payload, metric); err != nil {
			return nil, err
		}
		out = append(out, HistoryPoint{AgentID: row.AgentID, ObservedAt: row.Observed.UTC().Format(time.RFC3339Nano), Snapshot: metric.GetSnapshot()})
	}
	return out, nil
}

type Consumer struct {
	pull  *jetstream.PullConsumer
	store *Store
	dlq   DLQPublisher
}

// DLQPublisher receives poison host metric messages after validation fails.
// It is deliberately optional so unit tests and local memory-only runs can
// use the same consumer without a second EventBus client.
type DLQPublisher interface {
	Publish(context.Context, *messagepb.MooxMessage) error
}

type jetStreamDLQ struct{ client *jetstream.Client }

func NewDLQPublisher(client *jetstream.Client) DLQPublisher {
	if client == nil {
		return nil
	}
	return jetStreamDLQ{client: client}
}

func (p jetStreamDLQ) Publish(ctx context.Context, msg *messagepb.MooxMessage) error {
	if p.client == nil {
		return errors.New("host metric DLQ client is nil")
	}
	_, err := p.client.Publish(ctx, msg)
	return err
}

func Bind(ctx context.Context, client *jetstream.Client, store *Store) (*Consumer, error) {
	return bind(ctx, client, store, nil)
}

func BindWithDLQ(ctx context.Context, client *jetstream.Client, store *Store, dlq DLQPublisher) (*Consumer, error) {
	return bind(ctx, client, store, dlq)
}

func bind(ctx context.Context, client *jetstream.Client, store *Store, dlq DLQPublisher) (*Consumer, error) {
	if client == nil || store == nil {
		return nil, errors.New("host metrics client and store are required")
	}
	pull, err := client.BindPullConsumer(ctx, jetstream.ConsumerRef{Stream: Stream, Durable: Durable, FilterSubject: Topic, AckWait: 60 * time.Second, MaxDeliver: -1, MaxAckPending: 256, FetchMaxWait: time.Second})
	if err != nil {
		return nil, err
	}
	return &Consumer{pull: pull, store: store, dlq: dlq}, nil
}
func (c *Consumer) Close() error {
	if c == nil || c.pull == nil {
		return nil
	}
	return c.pull.Close()
}
func (c *Consumer) Run(ctx context.Context) error {
	for {
		ds, err := c.pull.Fetch(ctx, 64)
		for _, d := range ds {
			if handleErr := c.store.Ingest(ctx, d); handleErr != nil {
				if c.dlq != nil {
					if err := c.dlq.Publish(context.Background(), rejectionMessage(d, handleErr.Error())); err != nil {
						_ = d.Nak(context.Background(), time.Second)
						continue
					}
				}
				_ = d.Term(context.Background())
			}
		}
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, jetstream.ErrClosed) {
				return nil
			}
			return err
		}
	}
}

func rejectionMessage(delivery *jetstream.Delivery, reason string) *messagepb.MooxMessage {
	now := timestamppb.Now()
	id := "invalid-host-metric"
	topic := ""
	payload := []byte(nil)
	if delivery != nil {
		if delivery.RawMessageID != "" {
			id = delivery.RawMessageID
		}
		topic = delivery.Subject
		payload = append([]byte(nil), delivery.RawData...)
		if delivery.Message != nil && delivery.Message.GetMessageId() != "" {
			id = delivery.Message.GetMessageId()
		}
	}
	if id == "invalid-host-metric" {
		sum := sha256.Sum256(append([]byte(topic+"\x00"), payload...))
		id += "-" + hex.EncodeToString(sum[:8])
	}
	return &messagepb.MooxMessage{
		ProtocolVersion: 1,
		MessageId:       id + ".rejected",
		Topic:           DLQTopic,
		Kind:            messagepb.MessageKind_MESSAGE_KIND_EVENT,
		Producer:        &messagepb.Producer{ServiceName: "moox-monitor", InstanceId: "hostmetrics"},
		OccurredAt:      now,
		PublishedAt:     now,
		ContentType:     "application/octet-stream",
		Payload:         payload,
		Attributes: map[string]string{
			"rejection_reason":    reason,
			"original_topic":      topic,
			"original_message_id": id,
		},
	}
}
