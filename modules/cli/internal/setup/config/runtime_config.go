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

// RenderTradeDNSResolverConfig replaces only the Trade-owned dns_resolver
// mapping. The caller supplies the existing app.yaml bytes so unrelated
// runtime configuration remains untouched.
func RenderTradeDNSResolverConfig(snapshot *Snapshot, existing []byte) ([]byte, error) {
	return RenderTradeDNSResolverConfigForNode(snapshot, "", existing)
}

// RenderTradeDNSResolverConfigForNode disables the resolver on Trade nodes
// other than the single node selected by custom.toml. This prevents a
// control profile from advertising a second resolver endpoint.
func RenderTradeDNSResolverConfigForNode(snapshot *Snapshot, nodeID string, existing []byte) ([]byte, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("runtime_config: snapshot is required")
	}
	resolver := snapshot.Manifest.DNSResolver
	if nodeID = strings.TrimSpace(nodeID); nodeID != "" && resolver.Enabled && !strings.EqualFold(nodeID, resolver.TradeNode) {
		resolver.Enabled = false
		resolver.Domains = nil
	}
	fields := orderedMapping(
		mappingField{"enabled", resolver.Enabled},
		mappingField{"domains", normalizedDomains(resolver.Domains)},
		mappingField{"lookup_timeout_ms", resolver.LookupTimeoutMS},
		mappingField{"probe_timeout_ms", resolver.ProbeTimeoutMS},
		mappingField{"probe_port", resolver.ProbePort},
		mappingField{"cache_ttl_seconds", resolver.CacheTTLSeconds},
		mappingField{"max_ips_per_domain", resolver.MaxIPsPerDomain},
	)
	return replaceYAMLMapping(existing, "dns_resolver", fields)
}

// RenderCollectorDNSResolverConfig replaces only the Collector-owned
// dns_resolver mapping. The target is derived from the selected other_hosts
// entry, so the address is never duplicated in custom.toml or source code.
func RenderCollectorDNSResolverConfig(snapshot *Snapshot, existing []byte) ([]byte, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("runtime_config: snapshot is required")
	}
	resolver := snapshot.Manifest.DNSResolver
	nodeID, target, err := DNSResolverRuntimeTarget(snapshot)
	if err != nil {
		return nil, err
	}
	fields := orderedMapping(
		mappingField{"enabled", resolver.Enabled},
		mappingField{"target", target},
		mappingField{"node_id", nodeID},
		mappingField{"domains", normalizedDomains(resolver.Domains)},
		mappingField{"refresh_interval", durationSeconds(resolver.RefreshIntervalSeconds)},
		mappingField{"request_timeout", durationMilliseconds(resolver.RequestTimeoutMS)},
		mappingField{"cache_ttl", durationSeconds(resolver.CacheTTLSeconds)},
	)
	return replaceYAMLMapping(existing, "dns_resolver", fields)
}

// DNSResolverRuntimeTarget returns the non-secret node identity and native
// Gateway target used by Collector. It is also emitted by the CLI so deploy
// can register a route for a resolver hosted on a different node.
func DNSResolverRuntimeTarget(snapshot *Snapshot) (string, string, error) {
	if snapshot == nil {
		return "", "", fmt.Errorf("runtime_config: snapshot is required")
	}
	if !snapshot.Manifest.DNSResolver.Enabled {
		return "", "ip://127.0.0.1:11003", nil
	}
	host, err := findDNSResolverHost(snapshot.Manifest)
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(host.Name), "ip://" + net.JoinHostPort(strings.TrimSpace(host.Address), "11003"), nil
}

// WriteTradeDNSResolverConfig atomically writes a rendered Trade app.yaml.
func WriteTradeDNSResolverConfig(snapshot *Snapshot, path string) error {
	return writeRenderedConfig(path, func(existing []byte) ([]byte, error) {
		return RenderTradeDNSResolverConfig(snapshot, existing)
	})
}

// WriteCollectorDNSResolverConfig atomically writes a rendered Collector app.yaml.
func WriteCollectorDNSResolverConfig(snapshot *Snapshot, path string) error {
	return writeRenderedConfig(path, func(existing []byte) ([]byte, error) {
		return RenderCollectorDNSResolverConfig(snapshot, existing)
	})
}

// WriteRenderedRuntimeConfig atomically writes already-rendered YAML. It is
// used by the CLI after both Trade and Collector render operations have
// succeeded, so neither service is restarted with a half-rendered snapshot.
func WriteRenderedRuntimeConfig(path string, rendered []byte) error {
	return writeRenderedBytes(path, rendered)
}

func findDNSResolverHost(manifest Manifest) (Host, error) {
	name := strings.TrimSpace(manifest.DNSResolver.TradeNode)
	for _, host := range manifest.OtherHosts {
		if strings.EqualFold(strings.TrimSpace(host.Name), name) {
			if strings.TrimSpace(host.Address) == "" {
				return Host{}, fmt.Errorf("runtime_config: dns_resolver host %q has no address", name)
			}
			return host, nil
		}
	}
	return Host{}, fmt.Errorf("runtime_config: dns_resolver host %q was not found", name)
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

func writeRenderedConfig(path string, render func([]byte) ([]byte, error)) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("runtime_config: output path is required")
	}
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("runtime_config: read %s: %w", path, err)
	}
	rendered, err := render(existing)
	if err != nil {
		return err
	}
	return writeRenderedBytes(path, rendered)
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
