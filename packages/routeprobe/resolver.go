package routeprobe

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

type ResolveRequest struct {
	SCFRegion   string
	EgressScope string
	SourceKey   SourceKey
	Transport   Transport
	Host        string
	Port        int
	Hints       []AddressHint
}

type AddressHint struct {
	Address     string
	HintLatency time.Duration
}

type LookupHostFunc func(context.Context, string) ([]string, error)

// DNSResolver is an adapter over an existing DNS/route snapshot source. It
// only turns addresses into candidates; it does not listen on or implement a
// DNS server and does not claim that hint latency was observed by SCF.
type DNSResolver struct {
	LookupHost LookupHostFunc
}

func (resolver DNSResolver) Resolve(ctx context.Context, request ResolveRequest) ([]Candidate, error) {
	if strings.TrimSpace(request.Host) == "" {
		return nil, fmt.Errorf("host must not be empty")
	}
	if strings.TrimSpace(request.SCFRegion) == "" {
		return nil, fmt.Errorf("scf_region must not be empty")
	}
	if err := request.SourceKey.Validate(); err != nil {
		return nil, err
	}
	if !request.Transport.valid() {
		return nil, fmt.Errorf("unsupported transport %q", request.Transport)
	}
	if request.Port < 1 || request.Port > 65535 {
		return nil, fmt.Errorf("port %d must be between 1 and 65535", request.Port)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	lookup := resolver.LookupHost
	if lookup == nil {
		lookup = net.DefaultResolver.LookupHost
	}
	addresses, err := lookup(ctx, request.Host)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", request.Host, err)
	}
	return CandidatesFromAddresses(request, addresses)
}

func CandidatesFromAddresses(request ResolveRequest, addresses []string) ([]Candidate, error) {
	hints := make(map[string]time.Duration, len(request.Hints))
	for _, hint := range request.Hints {
		if hint.HintLatency < 0 {
			return nil, fmt.Errorf("hint latency for %q must not be negative", hint.Address)
		}
		if _, exists := hints[hint.Address]; !exists || hints[hint.Address] == 0 || (hint.HintLatency > 0 && hint.HintLatency < hints[hint.Address]) {
			hints[hint.Address] = hint.HintLatency
		}
	}
	candidates := make([]Candidate, 0, len(addresses))
	for _, address := range addresses {
		address = strings.TrimSpace(address)
		candidates = append(candidates, Candidate{
			SCFRegion: request.SCFRegion, EgressScope: request.EgressScope, SourceKey: request.SourceKey,
			Transport: request.Transport, Host: strings.TrimSpace(request.Host), Address: address, Port: request.Port,
			HintLatency: hints[address],
		})
	}
	return NormalizeCandidates(candidates)
}
