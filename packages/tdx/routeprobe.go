package tdx

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/packages/routeprobe"
)

// RouteProber adapts TDX's protocol-specific probe to the shared routeprobe
// orchestration. SourceID selects the handshake/login variant; candidates are
// never treated as interchangeable merely because they share port 7727.
type RouteProber struct {
	Timeout time.Duration
}

func (prober RouteProber) Probe(ctx context.Context, request routeprobe.ProbeRequest) (routeprobe.ProbeResult, error) {
	result := routeprobe.ProbeResult{Candidate: request.Candidate, Attempt: request.Attempt}
	if request.Candidate.Transport != routeprobe.TransportTCP {
		result.ErrorKind = routeprobe.ErrorUnsupported
		return result, fmt.Errorf("tdx: route probe requires tcp transport")
	}
	variant, err := variantForSource(request.Candidate.SourceKey.SourceID)
	if err != nil {
		result.ErrorKind = routeprobe.ErrorUnsupported
		return result, err
	}
	timeout := prober.Timeout
	if request.Timeout > 0 && (timeout <= 0 || request.Timeout < timeout) {
		timeout = request.Timeout
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	started := time.Now()
	err = Probe(ctx, ClientOptions{Host: request.Candidate.Address, Port: request.Candidate.Port, Variant: variant, Timeout: timeout})
	result.Latency = time.Since(started)
	result.FirstResponseLatency = result.Latency
	result.ObservedAt = started
	if err != nil {
		result.ErrorMessage = err.Error()
		return result, err
	}
	result.Success = true
	return result, nil
}

func variantForSource(sourceID string) (ProtocolVariant, error) {
	switch sourceID {
	case "normal_7709":
		return ProtocolNormal, nil
	case "ex_classic_7727":
		return ProtocolExClassic, nil
	case "ex_mac_7727":
		return ProtocolExMAC, nil
	default:
		return "", fmt.Errorf("tdx: unsupported SourceID %q", sourceID)
	}
}
