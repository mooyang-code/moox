package observability

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	"github.com/prometheus/client_golang/prometheus"
)

type nodeSource interface {
	ListSCFEventNodes(context.Context) ([]store.CloudNode, error)
}

type SCFMetrics struct {
	source             nodeSource
	now                func() time.Time
	nodes              *prometheus.GaugeVec
	oldestAge          prometheus.Gauge
	heartbeatTimestamp *prometheus.GaugeVec
	keepaliveRuns      *prometheus.CounterVec
}

func NewSCFMetrics(registerer prometheus.Registerer, source nodeSource) (*SCFMetrics, error) {
	if registerer == nil || source == nil {
		return nil, fmt.Errorf("SCF metrics require a registerer and node source")
	}
	metrics := &SCFMetrics{
		source: source,
		now:    time.Now,
		nodes: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "moox_cloudnode_scf_nodes",
			Help: "Current SCF node count grouped by heartbeat status.",
		}, []string{"status"}),
		oldestAge: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "moox_cloudnode_scf_oldest_heartbeat_age_seconds",
			Help: "Age in seconds of the oldest SCF node heartbeat.",
		}),
		heartbeatTimestamp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "moox_cloudnode_scf_heartbeat_timestamp_seconds",
			Help: "Last successful SCF heartbeat timestamp by node.",
		}, []string{"node_id", "function_name"}),
		keepaliveRuns: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "moox_cloudnode_scf_keepalive_runs_total",
			Help: "SCF keepalive maintenance runs grouped by result.",
		}, []string{"result"}),
	}
	if err := registerer.Register(metrics.nodes); err != nil {
		return nil, err
	}
	if err := registerer.Register(metrics.oldestAge); err != nil {
		return nil, err
	}
	if err := registerer.Register(metrics.heartbeatTimestamp); err != nil {
		return nil, err
	}
	if err := registerer.Register(metrics.keepaliveRuns); err != nil {
		return nil, err
	}
	return metrics, nil
}

func (m *SCFMetrics) Refresh(ctx context.Context) error {
	nodes, err := m.source.ListSCFEventNodes(ctx)
	if err != nil {
		return err
	}
	counts := map[string]float64{"online": 0, "timeout": 0, "unknown": 0}
	now := m.now().UTC()
	var oldest float64
	m.heartbeatTimestamp.Reset()
	for _, node := range nodes {
		status := store.SCFHeartbeatStatus(node.LastHeartbeatAt, now)
		counts[status]++
		heartbeatAt := float64(0)
		if node.LastHeartbeatAt != nil {
			age := max(now.Sub(node.LastHeartbeatAt.UTC()).Seconds(), 0)
			if age > oldest {
				oldest = age
			}
			heartbeatAt = float64(node.LastHeartbeatAt.UTC().Unix())
		}
		m.heartbeatTimestamp.WithLabelValues(node.NodeID, node.FunctionName).Set(heartbeatAt)
	}
	for status, count := range counts {
		m.nodes.WithLabelValues(status).Set(count)
	}
	m.oldestAge.Set(oldest)
	return nil
}

func (m *SCFMetrics) ObserveKeepalive(err error) {
	result := "success"
	if err != nil {
		result = "error"
	}
	m.keepaliveRuns.WithLabelValues(result).Inc()
}
