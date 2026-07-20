package doctor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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
	Client *http.Client
	Auth   HealthAuth
	Now    func() time.Time
}

func (p HTTPProber) Get(ctx context.Context, rawURL string) (ProbeResult, error) {
	if p.Auth.SecretKey == "" || p.Auth.AccessKey == "" {
		return ProbeResult{}, fmt.Errorf("health probe HMAC credentials are required")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ProbeResult{}, fmt.Errorf("probe URL must be an absolute HTTP URL without query or fragment")
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
	rsp, err := client.Do(req)
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

func ProbeWritablePath(ctx context.Context, releaseRoot, relativePath string) (err error) {
	if filepath.IsAbs(relativePath) || relativePath == "" || filepath.Clean(relativePath) != relativePath || strings.HasPrefix(relativePath, "..") {
		return fmt.Errorf("probe path must be a clean release-relative path")
	}
	root, err := filepath.Abs(releaseRoot)
	if err != nil {
		return err
	}
	target, err := filepath.Abs(filepath.Join(root, relativePath))
	if err != nil {
		return err
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve release root: %w", err)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return fmt.Errorf("resolve probe path: %w", err)
	}
	rel, err := filepath.Rel(resolvedRoot, resolvedTarget)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("probe path escapes release root")
	}
	file, err := os.CreateTemp(resolvedTarget, probePrefix)
	if err != nil {
		return err
	}
	name := file.Name()
	defer func() {
		closeErr := file.Close()
		removeErr := os.Remove(name)
		if err == nil && closeErr != nil {
			err = closeErr
		}
		if err == nil && removeErr != nil && !os.IsNotExist(removeErr) {
			err = removeErr
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err = file.WriteString("moox doctor permission probe\n")
	return err
}
