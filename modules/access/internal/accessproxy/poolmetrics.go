package accessproxy

import (
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	poolDialsDesc = prometheus.NewDesc("moox_gatewayclient_pool_dials_total",
		"Gateway client connection dials by source (request path or pool warm-up) and result.", []string{"source", "result"}, nil)
	connectRetriesDesc = prometheus.NewDesc("moox_gatewayclient_connect_retries_total",
		"Calls retried after a connection could not be obtained (request not sent), by outcome.", []string{"outcome"}, nil)
)

// poolStatsCollector 把网关客户端连接池的累计计数暴露为 Prometheus 计数器。
type poolStatsCollector struct {
	stats func() gatewayclient.PoolStats
}

// RegisterPoolMetrics 注册网关客户端连接池指标：请求路径上的新建连接数稳态应接近 0（连接由池预热），
// 预热拨号失败与建连重试结果用来判断跨主机链路是否健康。
func RegisterPoolMetrics(registerer prometheus.Registerer, stats func() gatewayclient.PoolStats) error {
	if registerer == nil {
		registerer = prometheus.DefaultRegisterer
	}
	return registerer.Register(poolStatsCollector{stats: stats})
}

func (poolStatsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- poolDialsDesc
	ch <- connectRetriesDesc
}

func (c poolStatsCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.stats()
	counter := func(desc *prometheus.Desc, value uint64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(desc, prometheus.CounterValue, float64(value), labels...)
	}
	counter(poolDialsDesc, s.RequestDialsOK, "request", "ok")
	counter(poolDialsDesc, s.RequestDialsErr, "request", "error")
	counter(poolDialsDesc, s.WarmupDialsOK, "warmup", "ok")
	counter(poolDialsDesc, s.WarmupDialsErr, "warmup", "error")
	counter(connectRetriesDesc, s.ConnectRetriesRecovered, "recovered")
	counter(connectRetriesDesc, s.ConnectRetriesFailed, "failed")
}
