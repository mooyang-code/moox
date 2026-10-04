package tdx

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
)

type Endpoint struct {
	Name    string          `json:"name"`
	Host    string          `json:"host"`
	Port    int             `json:"port"`
	Variant ProtocolVariant `json:"variant"`
}

func (endpoint Endpoint) Validate() error {
	endpoint.Host = strings.TrimSpace(endpoint.Host)
	if endpoint.Host == "" {
		return fmt.Errorf("tdx: endpoint host is required")
	}
	if strings.ContainsAny(endpoint.Host, " \t\r\n/\\") {
		return fmt.Errorf("tdx: endpoint host %q is invalid", endpoint.Host)
	}
	if strings.Contains(endpoint.Host, ":") && net.ParseIP(endpoint.Host) == nil {
		return fmt.Errorf("tdx: endpoint host %q must not include a port", endpoint.Host)
	}
	if endpoint.Port <= 0 || endpoint.Port > 65535 {
		return fmt.Errorf("tdx: endpoint %s has invalid port %d", endpoint.Host, endpoint.Port)
	}
	switch endpoint.Variant {
	case ProtocolNormal:
		if endpoint.Port != 7709 {
			return fmt.Errorf("tdx: normal source must use port 7709, got %d", endpoint.Port)
		}
	case ProtocolExClassic, ProtocolExMAC:
		if endpoint.Port != 7727 {
			return fmt.Errorf("tdx: extended source must use port 7727, got %d", endpoint.Port)
		}
	default:
		return fmt.Errorf("tdx: unsupported endpoint variant %q", endpoint.Variant)
	}
	return nil
}

type EndpointList []Endpoint

func (list EndpointList) Validate() error {
	seen := make(map[string]struct{}, len(list))
	for index, endpoint := range list {
		if err := endpoint.Validate(); err != nil {
			return fmt.Errorf("tdx: endpoint %d: %w", index, err)
		}
		key := fmt.Sprintf("%s/%s/%d", endpoint.Variant, endpoint.Host, endpoint.Port)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("tdx: duplicate endpoint %s", key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func LoadEndpoints(data []byte) (EndpointList, error) {
	var list EndpointList
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("tdx: decode endpoints: %w", err)
	}
	if err := list.Validate(); err != nil {
		return nil, err
	}
	return list, nil
}
