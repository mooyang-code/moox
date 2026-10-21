package config

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// RenderEgressConfig writes the independent HTTP and DNS upstream policy.
func RenderEgressConfig(snapshot *Snapshot, existing []byte) ([]byte, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("runtime_config: snapshot is required")
	}
	cfg := snapshot.Manifest.EgressProxy
	dns := cfg.DNS
	raw, err := replaceYAMLMapping(existing, "domains", valueNode(normalizedDomains(cfg.HTTPDomains)))
	if err != nil {
		return nil, err
	}
	return replaceYAMLMapping(raw, "dns", orderedMapping(
		mappingField{"domains", normalizedDomains(dns.Domains)},
		mappingField{"lookup_timeout_ms", dns.LookupTimeoutMS},
		mappingField{"probe_timeout_ms", dns.ProbeTimeoutMS},
		mappingField{"probe_port", dns.ProbePort},
		mappingField{"cache_ttl_seconds", dns.CacheTTLSeconds},
		mappingField{"max_ips_per_domain", dns.MaxIPsPerDomain},
	))
}

// RenderCollectorRuntimeConfig renders Collector's egress and SCF settings; the scf-collector key ID is filled in at install time from host material.
func RenderCollectorRuntimeConfig(snapshot *Snapshot, existing []byte) ([]byte, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("runtime_config: snapshot is required")
	}
	cfg := snapshot.Manifest.EgressProxy
	dns := cfg.DNS
	rendered, err := replaceYAMLMapping(existing, "egress_proxy", orderedMapping(
		mappingField{"domains", normalizedDomains(cfg.HTTPDomains)},
		mappingField{"dns", orderedMapping(
			mappingField{"domains", normalizedDomains(dns.Domains)},
			mappingField{"refresh_interval", durationSeconds(dns.RefreshIntervalSeconds)},
			mappingField{"request_timeout", durationMilliseconds(dns.RequestTimeoutMS)},
			mappingField{"cache_ttl", durationSeconds(dns.CacheTTLSeconds)},
		)},
	))
	if err != nil {
		return nil, err
	}
	rendered, err = replaceYAMLMapping(rendered, "dns_resolver", nil)
	if err != nil {
		return nil, err
	}
	rendered, err = replaceYAMLMapping(rendered, "scf_region_blacklists", nil)
	if err != nil {
		return nil, err
	}
	access := SCFAccessIdentity(snapshot)
	var prior map[string]any
	if len(existing) > 0 {
		if err := yaml.Unmarshal(existing, &prior); err != nil {
			return nil, err
		}
	}
	rendered, err = replaceYAMLMapping(rendered, "collector_runtime", nil)
	if err != nil {
		return nil, err
	}
	if storage, ok := prior["storage"].(map[string]any); ok {
		delete(storage, "gateway_target")
		rendered, err = replaceYAMLMapping(rendered, "storage", valueNode(storage))
		if err != nil {
			return nil, err
		}
	}
	rendered, err = replaceYAMLMapping(rendered, "scf_access", valueNode(access))
	if err != nil {
		return nil, err
	}
	rendered, err = replaceYAMLMapping(rendered, "scf_access_routes", nil)
	if err != nil {
		return nil, err
	}
	rendered, err = renderSCFPlanBlocks(snapshot.Manifest, rendered)
	if err != nil {
		return nil, err
	}
	retention := snapshot.Manifest.CollectorRetention
	rendered, err = replaceYAMLMapping(rendered, "collector_retention", orderedMapping(
		mappingField{"maintenance_interval", retention.MaintenanceInterval},
		mappingField{"maintenance_offset", retention.MaintenanceOffset},
		mappingField{"maintenance_timeout", retention.MaintenanceTimeout},
		mappingField{"max_rows_per_pass", retention.MaxRowsPerPass},
	))
	if err != nil {
		return nil, err
	}
	return rendered, nil
}

// renderSCFPlanBlocks 渲染 Timer 采集计划的 Collector 配置：scf_regions（地域网络）、scf_markets（空间与市场的运行地域）、
// scf_plan（云账号、代码包名称、命名空间、各地域 Access 私网入口）。未启用时三块一并移除，保证关闭后配置不残留。
// 代码包只写名称：Collector 总是使用该名称的最新可用版本，上传新版本后不需要改配置或重启。
func renderSCFPlanBlocks(manifest Manifest, rendered []byte) ([]byte, error) {
	plan := manifest.SCFPlan
	var err error
	if !plan.Enabled {
		for _, key := range []string{"scf_regions", "scf_markets", "scf_plan"} {
			if rendered, err = replaceYAMLMapping(rendered, key, nil); err != nil {
				return nil, err
			}
		}
		return rendered, nil
	}
	regions := map[string]map[string]string{}
	access := map[string]map[string]string{}
	for _, region := range manifest.SCFPlanRegionNames() {
		network := plan.Regions[region]
		regions[region] = map[string]string{"vpc_id": network.VPCID, "subnet_id": network.SubnetID}
		address, id, accessErr := manifest.SCFPlanAccess(region)
		if accessErr != nil {
			return nil, accessErr
		}
		access[region] = map[string]string{"access_address": address, "access_id": id}
	}
	markets := map[string]map[string][]string{}
	spaces := map[string]map[string]any{}
	for _, space := range plan.Spaces {
		markets[space.SpaceID] = space.Markets
		override := map[string]any{"package_name": space.PackageName, "namespace": space.Namespace,
			"default_ingest": space.DefaultIngest, "default_executor": space.DefaultExecutor}
		if space.SymbolsPerFunction > 0 {
			override["symbols_per_function"] = space.SymbolsPerFunction
		}
		spaces[space.SpaceID] = override
	}
	account := manifest.SCFCloudAccount()
	if rendered, err = replaceYAMLMapping(rendered, "scf_regions", valueNode(regions)); err != nil {
		return nil, err
	}
	if rendered, err = replaceYAMLMapping(rendered, "scf_markets", valueNode(markets)); err != nil {
		return nil, err
	}
	fields := []mappingField{
		{"cloud_account_id", account.AccountID},
		{"memory_mb", plan.MemoryMB},
		{"symbols_per_function", plan.SymbolsPerFunction},
	}
	if plan.AssumedEgressIPs > 0 {
		fields = append(fields, mappingField{"assumed_egress_ips", plan.AssumedEgressIPs})
	}
	fields = append(fields, mappingField{"access_routes", access}, mappingField{"spaces", spaces})
	return replaceYAMLMapping(rendered, "scf_plan", orderedMapping(fields...))
}

// WriteRenderedRuntimeConfig atomically writes already-rendered YAML.
func WriteRenderedRuntimeConfig(path string, rendered []byte) error {
	return writeRenderedBytes(path, rendered)
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
			if value == nil {
				root.Content = append(root.Content[:index], root.Content[index+2:]...)
			} else {
				root.Content[index+1] = value
			}
			return encodeYAML(document)
		}
	}
	if value != nil {
		root.Content = append(root.Content, scalarNode(key), value)
	}
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

func writeRenderedBytes(path string, rendered []byte) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("runtime_config: output path is required")
	}
	mode := os.FileMode(0o644)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("runtime_config: create %s: %w", dir, err)
	}
	temporary, err := os.CreateTemp(dir, ".moox-runtime-config-*")
	if err != nil {
		return fmt.Errorf("runtime_config: create temporary file: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return fmt.Errorf("runtime_config: chmod temporary file: %w", err)
	}
	if _, err := temporary.Write(rendered); err != nil {
		temporary.Close()
		return fmt.Errorf("runtime_config: write temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("runtime_config: sync temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("runtime_config: close temporary file: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("runtime_config: replace %s: %w", path, err)
	}
	return nil
}

// SCFAccessIdentity 返回函数调用 Access 的固定身份与默认入口（Storage 主机的 Access，缺省为控制机）；
// 各地域的私网入口由 [scf_plan] 渲染到 scf_plan.access_routes。
func SCFAccessIdentity(snapshot *Snapshot) map[string]string {
	base := map[string]string{"caller": "scf-collector", "key_id": "", "key_file": "../../secrets/caller-scf-collector.key"}
	host := snapshot.Manifest.StorageHost()
	if host.Address == "" {
		host = snapshot.Manifest.ControlHost()
	}
	if host.Address != "" {
		base["access_address"] = net.JoinHostPort(host.Address, "11004")
		base["access_id"] = "access@" + host.Name
	}
	return base
}
