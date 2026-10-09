package doctor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/requestauth"
)

const (
	probeTimeout  = 5 * time.Second
	maxProbeBytes = 1 << 20
	probePrefix   = ".moox-doctor-probe-"
)

type HealthAuth struct {
	Version, AccessKey, SecretKey string
}

type ProbeResult struct {
	StatusCode int
	Body       []byte
	Digest     string
	ObservedAt time.Time
}

type HTTPProber struct {
	Client    *http.Client
	Auth      HealthAuth
	Now       func() time.Time
	AllowHost func(string) bool
}

func (p HTTPProber) Get(ctx context.Context, rawURL string) (ProbeResult, error) {
	if p.Auth.SecretKey == "" || p.Auth.AccessKey == "" {
		return ProbeResult{}, fmt.Errorf("health probe HMAC credentials are required")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ProbeResult{}, fmt.Errorf("probe URL must be an absolute HTTP URL without query or fragment")
	}
	allowedHost := p.AllowHost
	if allowedHost == nil {
		allowedHost = localProbeHost
	}
	if !allowedHost(parsed.Hostname()) {
		return ProbeResult{}, fmt.Errorf("probe host %q is not allowed", parsed.Hostname())
	}
	if parsed.EscapedPath() != "/healthz" && parsed.EscapedPath() != "/readyz" && parsed.EscapedPath() != "/metrics" {
		return ProbeResult{}, fmt.Errorf("probe path %q is not allowed", parsed.EscapedPath())
	}
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return ProbeResult{}, err
	}
	now := time.Now().UTC()
	if p.Now != nil {
		now = p.Now().UTC()
	}
	nonce, err := requestauth.NewNonce()
	if err != nil {
		return ProbeResult{}, err
	}
	signature, err := requestauth.Sign(p.Auth.SecretKey, requestauth.Material{Method: http.MethodGet, Path: parsed.EscapedPath(), Timestamp: now.Unix(), Nonce: nonce})
	if err != nil {
		return ProbeResult{}, err
	}
	version := p.Auth.Version
	if version == "" {
		version = "moox-health-v1"
	}
	req.Header.Set("X-Moox-Health-Auth", strings.Join([]string{version, p.Auth.AccessKey, strconv.FormatInt(now.Unix(), 10), nonce, signature}, "/"))
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: probeTimeout}
	}
	clientCopy := *client
	clientCopy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return fmt.Errorf("redirects are not allowed for health probes")
	}
	rsp, err := clientCopy.Do(req)
	if err != nil {
		return ProbeResult{}, err
	}
	defer rsp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(rsp.Body, maxProbeBytes+1))
	if err != nil {
		return ProbeResult{}, err
	}
	if len(body) > maxProbeBytes {
		return ProbeResult{}, fmt.Errorf("probe response exceeds %d bytes", maxProbeBytes)
	}
	sum := sha256.Sum256(body)
	result := ProbeResult{StatusCode: rsp.StatusCode, Body: body, Digest: "sha256:" + hex.EncodeToString(sum[:]), ObservedAt: now}
	if rsp.StatusCode < 200 || rsp.StatusCode >= 300 {
		return result, fmt.Errorf("probe returned HTTP %s", rsp.Status)
	}
	return result, nil
}

func localProbeHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "localhost" || host == "localhost.localdomain" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
