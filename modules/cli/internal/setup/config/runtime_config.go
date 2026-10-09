package config

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// RenderCollectorRuntimeConfig 用 moox.toml 渲染 Collector app.yaml 中由部署决定的段落：出口代理、SCF 地域
// 黑名单、运行数据保留和 stockcn 容量，其余设置保持原样。
func RenderCollectorRuntimeConfig(snapshot *Snapshot, existing []byte) ([]byte, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("runtime_config: 缺少 moox.toml 快照")
	}
	egress := snapshot.Manifest.EgressProxy
	resolver := egress.DNS
	httpDomains, dnsDomains := []string{}, []string{}
	// 没有部署出口代理时，Collector 的请求全部直连。
	if len(snapshot.Manifest.HostsOf("egress-proxy")) > 0 {
		httpDomains = append(httpDomains, egress.HTTPDomains...)
		dnsDomains = normalizedDomains(resolver.Domains)
	}
	rendered, err := replaceYAMLMapping(existing, "egress_proxy", orderedMapping(
		mappingField{"domains", httpDomains},
		mappingField{"dns", orderedMapping(
			mappingField{"domains", dnsDomains},
			mappingField{"refresh_interval", durationSeconds(resolver.RefreshIntervalSeconds)},
			mappingField{"request_timeout", durationMilliseconds(resolver.RequestTimeoutMS)},
			mappingField{"cache_ttl", durationSeconds(resolver.CacheTTLSeconds)},
		)},
	))
	if err != nil {
		return nil, err
	}
	blacklists := make([]mappingField, 0, len(snapshot.Manifest.SCFFetcher.Spaces))
	for _, space := range snapshot.Manifest.SCFFetcher.Spaces {
		blacklists = append(blacklists, mappingField{space.SpaceID, append([]string{}, space.RegionBlacklist...)})
	}
	rendered, err = replaceYAMLMapping(rendered, "scf_region_blacklists", orderedMapping(blacklists...))
	if err != nil {
		return nil, err
	}
	retention := snapshot.Manifest.CollectorRetention
	rendered, err = replaceYAMLMapping(rendered, "collector_retention", orderedMapping(
		mappingField{"maintenance_interval", retention.MaintenanceInterval},
		mappingField{"maintenance_offset", retention.MaintenanceOffset},
		mappingField{"maintenance_timeout", retention.MaintenanceTimeout},
		mappingField{"max_rows_per_pass", retention.MaxRowsPerPass},
		mappingField{"execution_detail_retention", retention.ExecutionDetailRetention},
		mappingField{"scheduled_run_summary_retention", retention.ScheduledRunSummaryRetention},
		mappingField{"terminal_retry_retention", retention.TerminalRetryRetention},
		mappingField{"period_snapshot_retention", retention.PeriodSnapshotRetention},
	))
	if err != nil {
		return nil, err
	}
	for _, space := range snapshot.Manifest.SCFFetcher.Spaces {
		if !strings.EqualFold(strings.TrimSpace(space.SpaceID), "stockcn") {
			continue
		}
		stockFields := orderedMapping(
			mappingField{"expected_timer_function_count", space.TimerFunctionCount},
			mappingField{"measured_safe_group_size", space.MeasuredSafeGroupSize},
			mappingField{"stagger_start_second", space.StaggerStartSecond},
			mappingField{"stagger_window_seconds", space.StaggerWindowSeconds},
			mappingField{"stagger_max_starts_per_second", space.StaggerMaxStartsPerSecond},
		)
		return replaceYAMLMapping(rendered, "stockcn", stockFields)
	}
	return rendered, nil
}

func normalizedDomains(domains []string) []string {
	result := make([]string, len(domains))
	for i, domain := range domains {
		result[i] = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	}
	return result
}

func durationSeconds(value int) string {
	return fmt.Sprintf("%ds", value)
}

func durationMilliseconds(value int) string {
	return fmt.Sprintf("%dms", value)
}

type mappingField struct {
	key   string
	value any
}

func orderedMapping(fields ...mappingField) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, field := range fields {
		node.Content = append(node.Content, scalarNode(field.key), valueNode(field.value))
	}
	return node
}

func replaceYAMLMapping(existing []byte, key string, value *yaml.Node) ([]byte, error) {
	document := &yaml.Node{Kind: yaml.DocumentNode}
	if len(bytes.TrimSpace(existing)) == 0 {
		document.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
	} else if err := yaml.Unmarshal(existing, document); err != nil {
		return nil, fmt.Errorf("runtime_config: parse yaml: %w", err)
	}
	if len(document.Content) == 0 || document.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("runtime_config: yaml root must be a mapping")
	}
	root := document.Content[0]
	for index := 0; index+1 < len(root.Content); index += 2 {
		if root.Content[index].Value == key {
			root.Content[index+1] = value
			return encodeYAML(document)
		}
	}
	root.Content = append(root.Content, scalarNode(key), value)
	return encodeYAML(document)
}

func encodeYAML(document *yaml.Node) ([]byte, error) {
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		return nil, fmt.Errorf("runtime_config: encode yaml: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("runtime_config: close yaml encoder: %w", err)
	}
	return output.Bytes(), nil
}

func scalarNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func valueNode(value any) *yaml.Node {
	if node, ok := value.(*yaml.Node); ok {
		return node
	}
	data, err := yaml.Marshal(value)
	if err != nil {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil || len(document.Content) == 0 {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
	}
	return document.Content[0]
}
