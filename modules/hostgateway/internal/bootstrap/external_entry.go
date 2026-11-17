package bootstrap

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/mooyang-code/moox/modules/hostgateway/internal/config"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/snapshot"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/prometheus/client_golang/prometheus"
)

// snapshotDirectory 让进程内的网关客户端直接用本进程已经校验过的快照当目录，不必再连本机的 11002。
type snapshotDirectory struct{ state *snapshot.State }

func (s snapshotDirectory) Fetch(_ context.Context, version string) (gatewayclient.DirectoryUpdate, error) {
	view := s.state.Load()
	if view == nil {
		return gatewayclient.DirectoryUpdate{}, errors.New("host gateway snapshot not initialized")
	}
	update := gatewayclient.DirectoryUpdate{Version: view.Hash(), Changed: view.Hash() != version}
	if update.Changed {
		update.Hosts, update.Deployments = view.Topology()
	}
	return update, nil
}

// openEntryForwarder 建外部入口用的进程内网关客户端：以 host-gateway 的身份、用本主机的密钥签名，把目标不在本机的请求转给目标主机的 11003。
func openEntryForwarder(cfg config.Config, credentials gatewayauth.Credentials, state *snapshot.State) (*gatewayclient.Client, error) {
	client, err := gatewayclient.New(gatewayclient.Config{
		Mode: gatewayclient.Internal, Credentials: credentials, LocalHostID: cfg.Host.ID, CAFile: cfg.TLS.CAFile,
		Source: snapshotDirectory{state: state},
		// 整分钟突发：几十个采集函数在一两秒内同时写入。对每台目标主机保持 32 条连接常驻（预热），
		// 请求路径上不再现建跨地域的 TLS 连接，空闲上限 64 留出余量。
		Pool:           gatewayclient.PoolConfig{MinIdle: 32, MaxIdle: 64},
		OnRefreshError: func(err error) { log.Printf("host gateway external entry directory refresh failed: %v", err) },
	})
	if err != nil {
		return nil, err
	}
	// 跨主机转发失败的原因（建连失败、超时、对端重置）是丢整批采集的直接线索，失败本身不频繁，逐次记录。
	client.SetCallFailureObserver(func(f gatewayclient.CallFailure) {
		log.Printf("host gateway external entry forward failed service=%s method=%s target=%s attempt=%d connect_failed=%t retrying=%t remaining=%s err=%v",
			f.Service, f.Method, f.Target, f.Attempt, f.ConnectFailed, f.Retrying, f.Remaining.Round(time.Millisecond), f.Err)
	})
	if err := prometheus.DefaultRegisterer.Register(poolStatsCollector{stats: client.PoolStats}); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}

var (
	poolDialsDesc = prometheus.NewDesc("moox_gatewayclient_pool_dials_total",
		"Gateway client connections dialed (request path and warm-up together), by result.", []string{"result"}, nil)
	poolWarmupsDesc = prometheus.NewDesc("moox_gatewayclient_pool_warmups_total",
		"Connections obtained by the pool warm-up routine, by result.", []string{"result"}, nil)
	connectRetriesDesc = prometheus.NewDesc("moox_gatewayclient_connect_retries_total",
		"Calls retried after a connection could not be obtained (request not sent), by outcome.", []string{"outcome"}, nil)
)

// poolStatsCollector 把进程内网关客户端连接池的累计计数暴露为 Prometheus 计数器：预热到位后新建连接数稳态应接近 0，
// 拨号与预热失败、建连重试结果用来判断入口主机到目标主机的链路是否健康。
type poolStatsCollector struct {
	stats func() gatewayclient.PoolStats
}

func (poolStatsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- poolDialsDesc
	ch <- poolWarmupsDesc
	ch <- connectRetriesDesc
}

func (c poolStatsCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.stats()
	counter := func(desc *prometheus.Desc, value uint64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(desc, prometheus.CounterValue, float64(value), labels...)
	}
	counter(poolDialsDesc, s.DialsOK, "ok")
	counter(poolDialsDesc, s.DialsErr, "error")
	counter(poolWarmupsDesc, s.WarmupOK, "ok")
	counter(poolWarmupsDesc, s.WarmupErr, "error")
	counter(connectRetriesDesc, s.ConnectRetriesRecovered, "recovered")
	counter(connectRetriesDesc, s.ConnectRetriesFailed, "failed")
}
