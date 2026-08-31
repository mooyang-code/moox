package routeprobe

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Transport identifies the wire protocol used to validate a route. It is
// deliberately independent from a provider implementation.
type Transport string

const (
	TransportHTTP  Transport = "http"
	TransportHTTPS Transport = "https"
	TransportTCP   Transport = "tcp"
)

func (t Transport) String() string { return string(t) }

func (t Transport) valid() bool {
	return identifierPattern.MatchString(string(t))
}

// SourceKey identifies a concrete access channel within one provider. A
// SourceID is not a provider identity; for example, tdx/normal_7709 and
// tdx/ex_classic_7727 are two distinct SourceKeys.
type SourceKey struct {
	ProviderID string `json:"provider_id"`
	SourceID   string `json:"source_id"`
}

var identifierPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

func NewSourceKey(providerID, sourceID string) (SourceKey, error) {
	key := SourceKey{ProviderID: strings.ToLower(strings.TrimSpace(providerID)), SourceID: strings.ToLower(strings.TrimSpace(sourceID))}
	if err := key.Validate(); err != nil {
		return SourceKey{}, err
	}
	return key, nil
}

func (key SourceKey) Validate() error {
	if !identifierPattern.MatchString(key.ProviderID) {
		return fmt.Errorf("provider_id %q must be a lowercase URL-safe identifier", key.ProviderID)
	}
	if !identifierPattern.MatchString(key.SourceID) {
		return fmt.Errorf("source_id %q must be a lowercase URL-safe identifier", key.SourceID)
	}
	return nil
}

func (key SourceKey) String() string {
	if key.ProviderID == "" && key.SourceID == "" {
		return ""
	}
	return key.ProviderID + "/" + key.SourceID
}

// RouteKey is the isolation boundary for a route snapshot. The same host is
// intentionally isolated when its provider, SourceID, transport, or SCF
// region changes.
type RouteKey struct {
	SCFRegion string    `json:"scf_region"`
	SourceKey SourceKey `json:"source_key"`
	Transport Transport `json:"transport"`
	Host      string    `json:"host"`
	Port      int       `json:"port"`
}

func (key RouteKey) Validate() error {
	if strings.TrimSpace(key.SCFRegion) == "" {
		return errors.New("scf_region must not be empty")
	}
	if err := key.SourceKey.Validate(); err != nil {
		return err
	}
	if !key.Transport.valid() {
		return fmt.Errorf("unsupported transport %q", key.Transport)
	}
	if err := validateHost(key.Host); err != nil {
		return err
	}
	if key.Port < 1 || key.Port > 65535 {
		return fmt.Errorf("port %d must be between 1 and 65535", key.Port)
	}
	return nil
}

func (key RouteKey) String() string {
	if key.Host == "" {
		return strings.Join([]string{key.SCFRegion, key.SourceKey.String(), key.Transport.String()}, "/")
	}
	return strings.Join([]string{key.SCFRegion, key.SourceKey.String(), key.Transport.String(), net.JoinHostPort(key.Host, strconv.Itoa(key.Port))}, "/")
}

// Candidate is one concrete address for a logical endpoint. Host remains the
// logical hostname used for HTTP Host/SNI, while Address is the address used
// for the actual connection.
type Candidate struct {
	SCFRegion   string            `json:"scf_region,omitempty"`
	EgressScope string            `json:"egress_scope,omitempty"`
	SourceKey   SourceKey         `json:"source_key"`
	Transport   Transport         `json:"transport"`
	Host        string            `json:"host"`
	Address     string            `json:"address"`
	Port        int               `json:"port"`
	HintLatency time.Duration     `json:"hint_latency,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

func (candidate Candidate) RouteKey() RouteKey {
	host := strings.TrimSpace(candidate.Host)
	if host == "" {
		host = strings.TrimSpace(candidate.Address)
	}
	return RouteKey{
		SCFRegion: candidate.SCFRegion,
		SourceKey: candidate.SourceKey,
		Transport: candidate.Transport,
		Host:      host,
		Port:      candidate.Port,
	}
}

func (candidate Candidate) Validate() error {
	if strings.TrimSpace(candidate.SCFRegion) == "" {
		return errors.New("scf_region must not be empty")
	}
	if err := candidate.SourceKey.Validate(); err != nil {
		return err
	}
	if !candidate.Transport.valid() {
		return fmt.Errorf("unsupported transport %q", candidate.Transport)
	}
	if err := validateHost(candidate.Host); err != nil {
		return err
	}
	if err := validateAddress(candidate.Address); err != nil {
		return err
	}
	if candidate.Port < 1 || candidate.Port > 65535 {
		return fmt.Errorf("port %d must be between 1 and 65535", candidate.Port)
	}
	if candidate.HintLatency < 0 {
		return errors.New("hint_latency must not be negative")
	}
	return nil
}

func (candidate Candidate) DialAddress() string {
	address := strings.TrimSpace(candidate.Address)
	if address == "" {
		address = strings.TrimSpace(candidate.Host)
	}
	return net.JoinHostPort(address, strconv.Itoa(candidate.Port))
}

// Identity is stable across observations and excludes mutable health data.
func (candidate Candidate) Identity() string {
	return strings.Join([]string{
		candidate.SCFRegion,
		candidate.SourceKey.String(),
		candidate.Transport.String(),
		candidate.Host,
		candidate.DialAddress(),
	}, "|")
}

func (candidate Candidate) Equal(other Candidate) bool {
	return candidate.Identity() == other.Identity()
}

// NormalizeCandidates validates, stably de-duplicates, and defensively copies
// candidate metadata. A duplicate with a non-zero latency hint enriches the
// first occurrence without changing the caller's ordering.
func NormalizeCandidates(candidates []Candidate) ([]Candidate, error) {
	result := make([]Candidate, 0, len(candidates))
	seen := make(map[string]int, len(candidates))
	for index, candidate := range candidates {
		candidate.SCFRegion = strings.TrimSpace(candidate.SCFRegion)
		candidate.EgressScope = strings.TrimSpace(candidate.EgressScope)
		candidate.Host = strings.TrimSpace(candidate.Host)
		candidate.Address = strings.TrimSpace(candidate.Address)
		candidate.Transport = Transport(strings.ToLower(strings.TrimSpace(candidate.Transport.String())))
		if candidate.Host == "" {
			candidate.Host = candidate.Address
		}
		if candidate.Address == "" {
			candidate.Address = candidate.Host
		}
		if err := candidate.Validate(); err != nil {
			return nil, fmt.Errorf("candidate %d: %w", index, err)
		}
		candidate.Metadata = cloneStringMap(candidate.Metadata)
		identity := candidate.Identity()
		if existing, ok := seen[identity]; ok {
			if result[existing].HintLatency == 0 || (candidate.HintLatency > 0 && candidate.HintLatency < result[existing].HintLatency) {
				result[existing].HintLatency = candidate.HintLatency
			}
			if len(result[existing].Metadata) == 0 && len(candidate.Metadata) > 0 {
				result[existing].Metadata = candidate.Metadata
			}
			continue
		}
		seen[identity] = len(result)
		result = append(result, candidate)
	}
	return result, nil
}

func validateHost(host string) error {
	host = strings.TrimSpace(host)
	if host == "" || strings.ContainsAny(host, " \t\r\n") {
		return fmt.Errorf("host %q must be a nonempty hostname or IP address", host)
	}
	if strings.Contains(host, "/") || strings.Contains(host, "\\") {
		return fmt.Errorf("host %q must not contain a path", host)
	}
	if strings.Contains(host, ":") && net.ParseIP(host) == nil {
		return fmt.Errorf("host %q must not include a port", host)
	}
	return nil
}

func validateAddress(address string) error {
	address = strings.TrimSpace(address)
	if address == "" || strings.ContainsAny(address, " \t\r\n") {
		return fmt.Errorf("address %q must be a nonempty IP or hostname", address)
	}
	if strings.Contains(address, ":") && net.ParseIP(address) == nil {
		return fmt.Errorf("address %q must not include a port", address)
	}
	return nil
}

type ErrorKind string

const (
	ErrorNone        ErrorKind = ""
	ErrorTimeout     ErrorKind = "timeout"
	ErrorContext     ErrorKind = "context"
	ErrorConnection  ErrorKind = "connection"
	ErrorProtocol    ErrorKind = "protocol"
	ErrorRemote      ErrorKind = "remote"
	ErrorUnsupported ErrorKind = "unsupported"
	ErrorInvalid     ErrorKind = "invalid"
)

// ProbeRequest contains one concrete route attempt. Protocol-specific probes
// can use Metadata and Payload without adding provider coupling here.
type ProbeRequest struct {
	Candidate Candidate
	Attempt   int
	Timeout   time.Duration
	Payload   any
}

// ProbeResult is the normalized outcome of a protocol-aware validation.
type ProbeResult struct {
	Candidate            Candidate     `json:"candidate"`
	Attempt              int           `json:"attempt"`
	Success              bool          `json:"success"`
	Latency              time.Duration `json:"latency,omitempty"`
	FirstResponseLatency time.Duration `json:"first_response_latency,omitempty"`
	StatusCode           int           `json:"status_code,omitempty"`
	RemoteError          bool          `json:"remote_error,omitempty"`
	ErrorKind            ErrorKind     `json:"error_kind,omitempty"`
	ErrorMessage         string        `json:"error_message,omitempty"`
	ObservedAt           time.Time     `json:"observed_at"`
}

type HealthStatus string

const (
	StatusUnknown     HealthStatus = "unknown"
	StatusHealthy     HealthStatus = "healthy"
	StatusDegraded    HealthStatus = "degraded"
	StatusUnavailable HealthStatus = "unavailable"
)

// ScoredCandidate is a candidate paired with an explainable route score.
type ScoredCandidate struct {
	Candidate Candidate    `json:"candidate"`
	Stats     RouteStats   `json:"stats"`
	Score     float64      `json:"score"`
	Healthy   bool         `json:"healthy"`
	Status    HealthStatus `json:"status,omitempty"`
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneCandidate(candidate Candidate) Candidate {
	candidate.Metadata = cloneStringMap(candidate.Metadata)
	return candidate
}
