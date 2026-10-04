package routeprobe

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var ErrUnsupportedTransport = errors.New("routeprobe: unsupported transport")

type HTTPProbeResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

type HTTPProbeConfig struct {
	Method            string
	Scheme            string
	Path              string
	HostOverride      string
	ExpectedStatuses  []int
	RequestHeaders    http.Header
	TLSConfig         *tls.Config
	MaxBodyBytes      int64
	FollowRedirects   bool
	Body              []byte
	ResponseValidator func(HTTPProbeResponse) error
	DialContext       func(context.Context, string, string) (net.Conn, error)
}

// HTTPProbe performs a read-only HTTP request against the candidate address.
// The URL and TCP dial target are separate: HostOverride/candidate.Host is
// preserved as HTTP Host and TLS SNI, while candidate.Address is dialed.
type HTTPProbe struct {
	Config HTTPProbeConfig
}

func (probe HTTPProbe) Probe(ctx context.Context, request ProbeRequest) (ProbeResult, error) {
	candidate := request.Candidate
	result := ProbeResult{Candidate: candidate, Attempt: request.Attempt}
	if ctx == nil {
		ctx = context.Background()
	}
	cancel := func() {}
	if request.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, request.Timeout)
	}
	defer cancel()
	if !candidate.Transport.valid() {
		result.ErrorKind = ErrorUnsupported
		result.ErrorMessage = fmt.Sprintf("unsupported transport %q", candidate.Transport)
		return result, fmt.Errorf("%w: %s", ErrUnsupportedTransport, candidate.Transport)
	}
	if candidate.Transport != TransportHTTP && candidate.Transport != TransportHTTPS {
		result.ErrorKind = ErrorUnsupported
		result.ErrorMessage = fmt.Sprintf("HTTP probe cannot use %q", candidate.Transport)
		return result, fmt.Errorf("%w: %s", ErrUnsupportedTransport, candidate.Transport)
	}
	if strings.TrimSpace(candidate.Host) == "" {
		candidate.Host = candidate.Address
	}
	if strings.TrimSpace(candidate.Address) == "" {
		candidate.Address = candidate.Host
	}
	if err := candidate.SourceKey.Validate(); err != nil {
		result.ErrorKind = ErrorInvalid
		result.ErrorMessage = err.Error()
		return result, err
	}
	if err := validateHost(candidate.Host); err != nil {
		result.ErrorKind = ErrorInvalid
		result.ErrorMessage = err.Error()
		return result, err
	}
	if err := validateAddress(candidate.Address); err != nil {
		result.ErrorKind = ErrorInvalid
		result.ErrorMessage = err.Error()
		return result, err
	}
	result.Candidate = candidate
	if candidate.Port < 1 || candidate.Port > 65535 {
		result.ErrorKind = ErrorInvalid
		result.ErrorMessage = "candidate port must be between 1 and 65535"
		return result, errors.New(result.ErrorMessage)
	}
	scheme := strings.ToLower(strings.TrimSpace(probe.Config.Scheme))
	if scheme == "" {
		if candidate.Transport == TransportHTTPS {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	if (scheme == "https") != (candidate.Transport == TransportHTTPS) {
		result.ErrorKind = ErrorInvalid
		result.ErrorMessage = fmt.Sprintf("scheme %q does not match transport %q", scheme, candidate.Transport)
		return result, errors.New(result.ErrorMessage)
	}
	path := strings.TrimSpace(probe.Config.Path)
	if path == "" {
		path = "/"
	}
	pathURL, err := url.Parse(path)
	if err != nil || pathURL.IsAbs() || pathURL.Host != "" {
		result.ErrorKind = ErrorInvalid
		result.ErrorMessage = fmt.Sprintf("path %q must be a relative URL", path)
		return result, errors.New(result.ErrorMessage)
	}
	requestHost := strings.TrimSpace(probe.Config.HostOverride)
	if requestHost == "" {
		requestHost = candidate.Host
	}
	logicalHost := hostWithoutPort(requestHost)
	urlHost := net.JoinHostPort(candidate.Host, strconv.Itoa(candidate.Port))
	requestURL := (&url.URL{Scheme: scheme, Host: urlHost, Path: pathURL.Path, RawPath: pathURL.RawPath, RawQuery: pathURL.RawQuery, Fragment: pathURL.Fragment}).String()
	method := strings.ToUpper(strings.TrimSpace(probe.Config.Method))
	if method == "" {
		method = http.MethodGet
	}
	var requestBody io.Reader
	if probe.Config.Body != nil {
		requestBody = bytes.NewReader(probe.Config.Body)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, method, requestURL, requestBody)
	if err != nil {
		result.ErrorKind = ErrorInvalid
		result.ErrorMessage = err.Error()
		return result, err
	}
	httpRequest.Host = requestHost
	httpRequest.Header = cloneHeader(probe.Config.RequestHeaders)

	tlsConfig := probe.Config.TLSConfig
	if scheme == "https" {
		if tlsConfig == nil {
			tlsConfig = &tls.Config{}
		} else {
			tlsConfig = tlsConfig.Clone()
		}
		// The logical endpoint, not the resolved address, is the SNI name.
		tlsConfig.ServerName = logicalHost
	}
	dialContext := probe.Config.DialContext
	if dialContext == nil {
		dialer := &net.Dialer{}
		dialContext = dialer.DialContext
	}
	transport := &http.Transport{DialContext: func(dialCtx context.Context, network, _ string) (net.Conn, error) {
		return dialContext(dialCtx, network, candidate.DialAddress())
	}}
	if scheme == "https" {
		transport.TLSClientConfig = tlsConfig
	}
	client := &http.Client{Transport: transport}
	if !probe.Config.FollowRedirects {
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	defer transport.CloseIdleConnections()

	started := time.Now()
	firstResponse := time.Duration(0)
	trace := &httptrace.ClientTrace{GotFirstResponseByte: func() { firstResponse = time.Since(started) }}
	httpRequest = httpRequest.WithContext(httptrace.WithClientTrace(httpRequest.Context(), trace))
	response, err := client.Do(httpRequest)
	if err != nil {
		result.Latency = time.Since(started)
		result.FirstResponseLatency = firstResponse
		result.ErrorKind = classifyProbeError(ctx, err)
		result.ErrorMessage = err.Error()
		return result, err
	}
	defer response.Body.Close()
	result.StatusCode = response.StatusCode
	if firstResponse == 0 {
		firstResponse = time.Since(started)
	}
	result.FirstResponseLatency = firstResponse
	maxBodyBytes := probe.Config.MaxBodyBytes
	if maxBodyBytes <= 0 {
		maxBodyBytes = 1 << 20
	}
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes+1))
	result.Latency = time.Since(started)
	if readErr != nil {
		result.ErrorKind = ErrorRemote
		result.RemoteError = true
		result.ErrorMessage = readErr.Error()
		return result, readErr
	}
	if int64(len(responseBody)) > maxBodyBytes {
		err = fmt.Errorf("HTTP response exceeds %d bytes", maxBodyBytes)
		result.ErrorKind = ErrorRemote
		result.RemoteError = true
		result.ErrorMessage = err.Error()
		return result, err
	}
	if !matchesStatus(response.StatusCode, probe.Config.ExpectedStatuses) {
		result.ErrorKind = ErrorRemote
		result.RemoteError = true
		result.ErrorMessage = fmt.Sprintf("unexpected HTTP status %d", response.StatusCode)
		return result, nil
	}
	if probe.Config.ResponseValidator != nil {
		validationErr := probe.Config.ResponseValidator(HTTPProbeResponse{StatusCode: response.StatusCode, Header: response.Header.Clone(), Body: append([]byte(nil), responseBody...)})
		if validationErr != nil {
			result.ErrorKind = ErrorRemote
			result.RemoteError = true
			result.ErrorMessage = validationErr.Error()
			return result, nil
		}
	}
	result.Success = true
	return result, nil
}

func matchesStatus(status int, expected []int) bool {
	if len(expected) == 0 {
		return status >= http.StatusOK && status < http.StatusMultipleChoices
	}
	for _, item := range expected {
		if status == item {
			return true
		}
	}
	return false
}

func hostWithoutPort(host string) string {
	host = strings.TrimSpace(host)
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		return strings.Trim(parsed, "[]")
	}
	return strings.Trim(host, "[]")
}

func cloneHeader(header http.Header) http.Header {
	if header == nil {
		return make(http.Header)
	}
	return header.Clone()
}
