// Package gatewayclient provides signed object calls and raw tRPC forwarding
// through the host gateway, fixed external access, or SSH tunnels.
package gatewayclient

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
)

const (
	Internal = "local"
	External = "access"
	Tunnel   = "tunnel"
)

// TunnelResolver connects to a host selected by ID using trusted SSH settings.
// Returned addresses must be local forwards to the host's 127.0.0.1:11002.
type TunnelResolver interface {
	Resolve(ctx context.Context, hostID string) (string, error)
}

// Invoker is the object-call interface used by service clients.
type Invoker interface {
	Invoke(context.Context, string, string, any, any) error
}

type Config struct {
	Mode             string
	Credentials      gatewayauth.Credentials
	LocalHostID      string
	LocalAddress     string
	CAFile           string
	Source           DirectorySource
	CachePath        string
	Tunnels          TunnelResolver
	AccessAddress    string
	AccessInstanceID string
	Serialization    int
	Timeout          time.Duration
	DirectoryTimeout time.Duration
	RefreshInterval  time.Duration
	OnRefreshError   func(error)
}

type endpoint struct {
	address, target, caFile, serverName string
}

type invokeFunc func(context.Context, endpoint, string, string, int, []byte, http.Header) ([]byte, error)

type Client struct {
	config      Config
	catalog     servicecatalog.Catalog
	mu          sync.RWMutex
	directory   servicecatalog.Directory
	refresh     chan struct{}
	invoke      invokeFunc
	pool        *rpcPool
	ownedSource io.Closer
	cancel      context.CancelFunc
	done        chan struct{}
	closed      atomic.Bool
}

var hostID = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)

func New(config Config) (*Client, error) {
	if config.DirectoryTimeout == 0 {
		config.DirectoryTimeout = 5 * time.Second
	}
	if config.RefreshInterval == 0 {
		// Directory publication follows the host snapshot pull. Leave room for
		// both polling stages within the 15-second deployment visibility budget.
		config.RefreshInterval = 5 * time.Second
	}
	if config.Timeout < 0 || config.DirectoryTimeout <= 0 || config.RefreshInterval <= 0 || config.Serialization != codec.SerializationTypePB && config.Serialization != codec.SerializationTypeJSON {
		return nil, fmt.Errorf("gateway client requires positive timeouts and PB or JSON serialization")
	}
	if config.Credentials.Caller == "" {
		return nil, fmt.Errorf("gateway caller is required")
	}
	if _, err := gatewayauth.Sign(config.Credentials, gatewayauth.Request{Method: "POST", Path: "/validate", TargetNode: "bootstrap"}, time.Now()); err != nil {
		return nil, err
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return nil, err
	}
	var ownedSource io.Closer
	switch config.Mode {
	case Internal:
		if !hostID.MatchString(config.LocalHostID) {
			return nil, fmt.Errorf("internal client requires local host ID")
		}
		if config.LocalAddress == "" {
			config.LocalAddress = "127.0.0.1:11002"
		}
		if !loopbackAddress(config.LocalAddress) {
			return nil, fmt.Errorf("local gateway must use a loopback address")
		}
		if config.CAFile == "" || config.CAFile == "none" || config.CAFile == "root" {
			return nil, fmt.Errorf("internal client requires the MooX private CA file")
		}
		pem, err := os.ReadFile(config.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read MooX CA: %w", err)
		}
		if !x509.NewCertPool().AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("MooX CA file contains no certificates")
		}
		if config.Source == nil {
			source, err := NewLocalDirectorySource(config.LocalAddress, config.DirectoryTimeout)
			if err != nil {
				return nil, err
			}
			config.Source, ownedSource = source, source
		}
	case Tunnel:
		if config.Credentials.Caller != "moox-cli" || config.Source == nil || config.Tunnels == nil {
			return nil, fmt.Errorf("tunnel client requires moox-cli identity, directory source and tunnel resolver")
		}
	case External:
		if err := validateExternalIdentity(catalog, config.Credentials.Caller, config.AccessAddress, config.AccessInstanceID); err != nil {
			return nil, err
		}
		if config.Source != nil || config.CachePath != "" || config.Tunnels != nil {
			return nil, fmt.Errorf("external client does not use directory discovery")
		}
	default:
		return nil, fmt.Errorf("unknown gateway client mode")
	}
	pool := newRPCPool()
	c := &Client{config: config, catalog: catalog, pool: pool, ownedSource: ownedSource, invoke: pool.invoke, done: make(chan struct{}), refresh: make(chan struct{}, 1)}
	if config.Mode != External {
		if config.CachePath != "" {
			if cached, err := readCache(config.CachePath); err == nil {
				c.directory = cached
			}
		}
		err := c.Refresh(context.Background())
		if c.directory.Version == "" {
			_ = pool.Close()
			if ownedSource != nil {
				_ = ownedSource.Close()
			}
			return nil, fmt.Errorf("initial directory unavailable: %w", err)
		}
		if err != nil {
			c.report(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	go c.poll(ctx)
	return c, nil
}

func (c *Client) Directory() servicecatalog.Directory {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.directory.Clone()
}

// LocalHostID is the identity loaded from this internal client's host gateway
// configuration. External clients have no local host identity.
func (c *Client) LocalHostID() string { return c.config.LocalHostID }

func (c *Client) Refresh(ctx context.Context) error {
	if c.closed.Load() {
		return net.ErrClosed
	}
	if c.config.Mode == External {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, c.config.DirectoryTimeout)
	defer cancel()
	select {
	case c.refresh <- struct{}{}:
		defer func() { <-c.refresh }()
	case <-ctx.Done():
		return ctx.Err()
	}
	current := c.Directory().Version
	update, err := c.config.Source.Fetch(ctx, current)
	if err != nil {
		return err
	}
	if !update.Changed {
		if current == "" || update.Directory.Version != current {
			return fmt.Errorf("unchanged directory has an unexpected version")
		}
		return nil
	}
	if err := update.Directory.Validate(); err != nil {
		return err
	}
	directory := update.Directory.Clone()
	c.mu.Lock()
	c.directory = directory
	c.mu.Unlock()
	// A disk failure must not keep a disabled host in the active in-memory map.
	if err := writeCache(c.config.CachePath, directory); err != nil {
		return fmt.Errorf("persist directory: %w", err)
	}
	return nil
}

func (c *Client) poll(ctx context.Context) {
	defer close(c.done)
	if c.config.Mode == External {
		<-ctx.Done()
		return
	}
	ticker := time.NewTicker(c.config.RefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.Refresh(ctx); err != nil && ctx.Err() == nil {
				c.report(err)
			}
		}
	}
}

func (c *Client) report(err error) {
	if c.config.OnRefreshError != nil {
		c.config.OnRefreshError(err)
	}
}

func (c *Client) Close() error {
	c.closed.Store(true)
	c.cancel()
	err := c.pool.Close()
	if c.ownedSource != nil {
		err = errors.Join(err, c.ownedSource.Close())
	}
	timer := time.NewTimer(c.config.DirectoryTimeout)
	defer timer.Stop()
	select {
	case <-c.done:
		return err
	case <-timer.C:
		return errors.Join(err, fmt.Errorf("directory source did not stop within client timeout"))
	}
}

func (c *Client) Invoke(ctx context.Context, service, method string, req, rsp interface{}) error {
	body, err := codec.Marshal(c.config.Serialization, req)
	if err != nil {
		return err
	}
	response, err := c.Forward(ctx, service, method, c.config.Serialization, body)
	if err != nil {
		return err
	}
	if err := codec.Unmarshal(c.config.Serialization, response, rsp); err != nil {
		return errs.NewFrameError(errs.RetClientDecodeFail, "decode gateway response")
	}
	return nil
}

func (c *Client) Forward(ctx context.Context, service, method string, serialization int, body []byte) ([]byte, error) {
	if c.closed.Load() {
		return nil, net.ErrClosed
	}
	if serialization != codec.SerializationTypePB && serialization != codec.SerializationTypeJSON {
		return nil, fmt.Errorf("forward requires PB or JSON serialization")
	}
	allowed := c.catalog.Allowed(c.config.Credentials.Caller, service, method)
	if c.config.Mode == External {
		allowed = c.catalog.PrincipalAllowed(c.config.Credentials.Caller, service, method)
	}
	if !allowed {
		return nil, fmt.Errorf("gateway caller is not allowed for this method")
	}
	spec, _ := c.catalog.Service(service)
	limit := spec.MaxBodyBytes
	if limit == 0 {
		limit = servicecatalog.DefaultMaxBodyBytes
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("gateway request exceeds service body limit")
	}
	timeout := c.config.Timeout
	if timeout == 0 {
		timeout = time.Duration(spec.TimeoutMS) * time.Millisecond
	}
	if timeout == 0 {
		timeout = time.Duration(servicecatalog.DefaultTimeoutMS) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	failedHost := ""
	for attempt := 0; attempt < 2; attempt++ {
		ep, err := c.endpoint(ctx, service, failedHost)
		if err != nil {
			return nil, err
		}
		headers, err := gatewayauth.Sign(c.config.Credentials, gatewayauth.Request{Method: "POST", Path: "/" + service + "/" + method, TargetNode: ep.target, Caller: c.config.Credentials.Caller, Callee: service, Func: method, Body: body}, time.Now())
		if err != nil {
			return nil, err
		}
		metadata := CallMetadataFromContext(ctx)
		for name, value := range map[string]string{
			"X-Space-Id": metadata.SpaceID, "X-User-Id": metadata.UserID,
			"X-User-Role": metadata.UserRole, "X-Trace-Id": metadata.TraceID,
		} {
			if value != "" {
				headers.Set(name, value)
			}
		}
		response, err := c.invoke(ctx, ep, service, method, serialization, body, headers)
		if err == nil {
			if int64(len(response)) > limit {
				return nil, fmt.Errorf("gateway response exceeds service body limit")
			}
			return response, nil
		}
		code := errs.Code(err)
		refresh := code == errs.RetClientNetErr || code == errs.RetServerNoService
		retry := refresh || code == errs.RetClientTimeout
		if refresh && c.config.Mode != External {
			if refreshErr := c.Refresh(ctx); refreshErr != nil {
				c.report(refreshErr)
			}
		}
		if attempt == 1 || !retry || !c.catalog.ReadOnly(service, method) || ctx.Err() != nil {
			return nil, err
		}
		failedHost = ep.target
	}
	panic("unreachable gateway retry attempt")
}

func (c *Client) endpoint(ctx context.Context, service, previous string) (endpoint, error) {
	if c.config.Mode == External {
		return endpoint{address: c.config.AccessAddress, target: c.config.AccessInstanceID}, nil
	}
	c.mu.RLock()
	// Published directory maps are immutable. Keep one coherent snapshot without
	// copying the entire fleet on every RPC.
	directory := c.directory
	c.mu.RUnlock()
	hosts := slices.Clone(directory.Services[service])
	if len(hosts) == 0 {
		return endpoint{}, fmt.Errorf("service has no enabled deployment")
	}
	slices.Sort(hosts)
	if i := slices.Index(hosts, c.config.LocalHostID); i >= 0 {
		hosts = append([]string{hosts[i]}, append(hosts[:i], hosts[i+1:]...)...)
	}
	chosen := hosts[0]
	if chosen == previous && len(hosts) > 1 {
		chosen = hosts[1]
	}
	if c.config.Mode == Tunnel {
		address, err := c.config.Tunnels.Resolve(ctx, chosen)
		if err != nil {
			return endpoint{}, err
		}
		if !loopbackAddress(address) {
			return endpoint{}, fmt.Errorf("SSH tunnel returned a non-loopback endpoint")
		}
		return endpoint{address: address, target: chosen}, nil
	}
	if chosen == c.config.LocalHostID {
		return endpoint{address: c.config.LocalAddress, target: chosen}, nil
	}
	host := directory.Hosts[chosen]
	return endpoint{address: net.JoinHostPort(host.Address, "11003"), target: chosen, caFile: c.config.CAFile, serverName: chosen}, nil
}

func validAddress(address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" || strings.ContainsAny(host, "/@?# \t\r\n") {
		return false
	}
	n, err := strconv.Atoi(port)
	return err == nil && n > 0 && n <= 65535
}

func loopbackAddress(address string) bool {
	if !validAddress(address) {
		return false
	}
	host, _, _ := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
