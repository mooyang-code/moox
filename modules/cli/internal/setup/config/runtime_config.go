package config

import (
	"fmt"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
)

// RenderEgressConfig 写出口代理的 HTTP 域名白名单；渲染在新包里，这里只把旧结构转过去。
func RenderEgressConfig(snapshot *Snapshot, existing []byte) ([]byte, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("runtime_config: snapshot is required")
	}
	return topology.RenderEgressConfig(snapshot.Manifest.TopologyConfig(), existing)
}

// RenderCollectorRuntimeConfig 渲染 Collector 的出口、SCF 与保留设置；渲染在新包里。
func RenderCollectorRuntimeConfig(snapshot *Snapshot, existing []byte) ([]byte, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("runtime_config: snapshot is required")
	}
	return topology.RenderCollectorRuntimeConfig(snapshot.Manifest.TopologyConfig(), existing)
}

// SCFAccessIdentity 返回函数调用 Access 的固定身份与默认入口。
func SCFAccessIdentity(snapshot *Snapshot) map[string]string {
	return topology.SCFAccessIdentity(snapshot.Manifest.TopologyConfig())
}

// TopologyConfig 把旧结构里属于拓扑的部分转成新包的配置，给渲染函数和已经切到新包的命令用（SSH 密码不带过去）。
func (m Manifest) TopologyConfig() *topology.Config {
	hosts := make(map[string]topology.HostDefinition, len(m.HostCatalog))
	for id, host := range m.HostCatalog {
		hosts[id] = topology.HostDefinition{
			Label: host.Label, Address: host.Address, PrivateAddress: host.PrivateAddress, Region: host.Region,
			Provider: host.Provider, Reachability: host.Reachability,
			SSH: topology.SSHConfig{Port: host.SSH.Port, Username: host.SSH.Username, PrivateKeyFile: host.SSH.PrivateKeyFile},
		}
	}
	return &topology.Config{
		Paths:              topology.Paths{DeployRoot: m.Paths.DeployRoot},
		EventBus:           topology.EventBus{Port: m.EventBus.Port},
		HostCatalog:        hosts,
		Deployments:        m.Deployments,
		StorageRetention:   m.StorageRetention,
		StorageView:        m.StorageView,
		CollectorRetention: m.CollectorRetention,
		EgressProxy:        m.EgressProxy,
		Logging: topology.Logging{RetentionDays: m.Logging.RetentionDays, CLS: topology.LoggingCLS{
			Enabled: m.Logging.CLS.Enabled, Endpoint: m.Logging.CLS.Endpoint, TopicID: m.Logging.CLS.TopicID, MinLevel: m.Logging.CLS.MinLevel,
		}},
		SCFPlan:            m.SCFPlan,
		ArchiveObjectStore: m.ArchiveObjectStore,
	}
}

// Config 同 Manifest.TopologyConfig；快照为空时返回 nil，调用方按“没有配置”处理（例如自己读文件）。
func (s *Snapshot) Config() *topology.Config {
	if s == nil {
		return nil
	}
	return s.Manifest.TopologyConfig()
}
