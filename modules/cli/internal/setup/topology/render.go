package topology

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gopkg.in/yaml.v3"
)

// RenderEgressConfig 在出口代理配置里写入 HTTP 域名白名单。
func RenderEgressConfig(cfg *Config, existing []byte) ([]byte, error) {
	if cfg == nil {
		return nil, errors.New("runtime_config: config is required")
	}
	return replaceYAMLMapping(existing, "domains", valueNode(normalizedDomains(cfg.EgressProxy.HTTPDomains)))
}

// RenderCollectorRuntimeConfig 在 Collector 配置里写入出口代理、SCF 采集计划与执行记录保留；scf-collector 的私钥路径在部署渲染时换成绝对路径。
func RenderCollectorRuntimeConfig(cfg *Config, existing []byte) ([]byte, error) {
	if cfg == nil {
		return nil, errors.New("runtime_config: config is required")
	}
	rendered, err := replaceYAMLMapping(existing, "egress_proxy", orderedMapping(
		mappingField{"domains", normalizedDomains(cfg.EgressProxy.HTTPDomains)},
	))
	if err != nil {
		return nil, err
	}
	// 下架自动检测要经出口代理访问币安、OKX：没配海外机时关掉，免得三个来源一直失败；手工登记不受影响。
	// 只替换模板里已有的 delist 段、不新增：模板（config/app.yaml）自带这一段，全新安装照常写入。
	rendered, err = replaceExistingYAMLMapping(rendered, "delist", orderedMapping(
		mappingField{"auto_detect", cfg.HasEgressProxy()},
	))
	if err != nil {
		return nil, err
	}
	rendered, err = replaceYAMLMapping(rendered, "scf_region_blacklists", nil)
	if err != nil {
		return nil, err
	}
	entry := SCFEntryIdentity(cfg)
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
	rendered, err = replaceYAMLMapping(rendered, "scf_entry", valueNode(entry))
	if err != nil {
		return nil, err
	}
	rendered, err = renderSCFPlanBlocks(cfg, rendered)
	if err != nil {
		return nil, err
	}
	retention := cfg.CollectorRetention
	return replaceYAMLMapping(rendered, "collector_retention", orderedMapping(
		mappingField{"maintenance_interval", retention.MaintenanceInterval},
		mappingField{"maintenance_offset", retention.MaintenanceOffset},
		mappingField{"maintenance_timeout", retention.MaintenanceTimeout},
		mappingField{"max_rows_per_pass", retention.MaxRowsPerPass},
	))
}

// renderSCFPlanBlocks 渲染 Timer 采集计划的 Collector 配置：scf_regions（地域网络：vpc 或 public）、scf_markets（空间与市场的运行地域）、
// scf_plan（云账号、代码包名称、命名空间、各地域外部入口的地址：vpc 地域是私网地址，public 地域是公网地址）。未启用时三块一并移除：这只关掉云函数，推送采集不受影响。
// 代码包只写名称：Collector 总是使用该名称的最新可用版本，上传新版本后不需要改配置或重启。
func renderSCFPlanBlocks(cfg *Config, rendered []byte) ([]byte, error) {
	plan := cfg.SCFPlan
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
	entries := map[string]map[string]string{}
	// 渲染配置里的全部地域（含没被市场使用的）：撤掉地域时，退役函数在计划推进期间还要用到它的网络与入口。
	for _, region := range cfg.SCFPlanConfiguredRegions() {
		network := plan.Regions[region]
		if network.Network == SCFNetworkPublic {
			regions[region] = map[string]string{"network": SCFNetworkPublic}
		} else {
			regions[region] = map[string]string{"vpc_id": network.VPCID, "subnet_id": network.SubnetID}
		}
		address, host, entryErr := cfg.SCFPlanEntry(region)
		if entryErr != nil {
			return nil, entryErr
		}
		entries[region] = map[string]string{"entry_address": address, "entry_host": host}
	}
	markets := map[string]map[string][]string{}
	spaces := map[string]map[string]any{}
	for _, space := range plan.Spaces {
		markets[space.SpaceID] = space.Markets
		override := map[string]any{"namespace": space.Namespace}
		if space.SymbolsPerFunction > 0 {
			override["symbols_per_function"] = space.SymbolsPerFunction
		}
		if space.BackfillRegion != "" {
			override["backfill_region"] = space.BackfillRegion
		}
		spaces[space.SpaceID] = override
	}
	if rendered, err = replaceYAMLMapping(rendered, "scf_regions", valueNode(regions)); err != nil {
		return nil, err
	}
	if rendered, err = replaceYAMLMapping(rendered, "scf_markets", valueNode(markets)); err != nil {
		return nil, err
	}
	fields := []mappingField{
		{"cloud_account_id", plan.CloudAccount.AccountID},
		{"package_name", plan.PackageName},
		{"memory_mb", plan.MemoryMB},
		{"symbols_per_function", plan.SymbolsPerFunction},
	}
	if plan.BackfillReservedConcurrency > 0 {
		fields = append(fields, mappingField{"backfill_reserved_concurrency", plan.BackfillReservedConcurrency})
	}
	fields = append(fields, mappingField{"entry_routes", entries}, mappingField{"spaces", spaces})
	return replaceYAMLMapping(rendered, "scf_plan", orderedMapping(fields...))
}

// SCFEntryIdentity 返回函数的固定签名身份与默认的外部入口（见 Config.EntryHost）；
// 各地域的入口（vpc 地域是私网地址，public 地域是公网地址）由 [scf_plan] 渲染到 scf_plan.entry_routes。没有主机标外部入口时不写入口，保留模板里的占位。
func SCFEntryIdentity(cfg *Config) map[string]string {
	base := map[string]string{"caller": "scf-collector", "key_file": "../../secrets/scf-collector.key"}
	if host := cfg.EntryHost(); host.Address != "" {
		base["entry_address"] = net.JoinHostPort(host.Address, strconv.Itoa(servicecatalog.HostGatewayExternalPort))
		base["entry_host"] = host.Name
	}
	return base
}

func normalizedDomains(domains []string) []string {
	result := make([]string, len(domains))
	for i, domain := range domains {
		result[i] = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	}
	return result
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

// replaceExistingYAMLMapping 只替换顶层已有的键；没有这个键时原样返回，不新增。
func replaceExistingYAMLMapping(existing []byte, key string, value *yaml.Node) ([]byte, error) {
	var top map[string]any
	if err := yaml.Unmarshal(existing, &top); err != nil {
		return nil, fmt.Errorf("runtime_config: parse yaml: %w", err)
	}
	if _, ok := top[key]; !ok {
		return existing, nil
	}
	return replaceYAMLMapping(existing, key, value)
}

func replaceYAMLMapping(existing []byte, key string, value *yaml.Node) ([]byte, error) {
	document := &yaml.Node{Kind: yaml.DocumentNode}
	if len(bytes.TrimSpace(existing)) == 0 {
		document.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
	} else if err := yaml.Unmarshal(existing, document); err != nil {
		return nil, fmt.Errorf("runtime_config: parse yaml: %w", err)
	}
	if len(document.Content) == 0 || document.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("runtime_config: yaml root must be a mapping")
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
