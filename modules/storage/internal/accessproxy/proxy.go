package accessproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/filter"
	"trpc.group/trpc-go/trpc-go/server"
)

const (
	AccessServiceName = "trpc.moox.storage.Access"
	PrimaryStoreName  = "trpc.moox.storage.PrimaryStore"
	MetadataName      = "trpc.moox.storage.Metadata"
	DataViewName      = "trpc.moox.storage.DataView"

	defaultMaxBodyBytes = 32 << 20
	defaultTimeout      = 30 * time.Second
	defaultNonceNS      = "storage-access"
)

// NonceStore persists inbound gateway nonces so a process restart does not
// reopen the replay window.
type NonceStore interface {
	Consume(context.Context, string, string, time.Duration) (bool, error)
}

// Invoker is the small part of the tRPC client needed by the raw proxy.
type Invoker interface {
	Invoke(context.Context, interface{}, interface{}, ...client.Option) error
}

type Options struct {
	InboundCredentials  gatewayauth.Credentials
	UpstreamCredentials gatewayauth.Credentials
	InboundTargetNode   string
	UpstreamTargetNode  string
	UpstreamTarget      string
	AllowedCallers      []string
	Nonces              NonceStore
	NonceNamespace      string
	MaxBodyBytes        int64
	Timeout             time.Duration
	Now                 func() time.Time
	Invoker             Invoker
}

type Proxy struct {
	inboundCredentials  gatewayauth.Credentials
	upstreamCredentials gatewayauth.Credentials
	inboundTargetNode   string
	upstreamTargetNode  string
	upstreamTarget      string
	allowedCallers      map[string]struct{}
	nonces              NonceStore
	nonceNamespace      string
	maxBodyBytes        int64
	timeout             time.Duration
	now                 func() time.Time
	invoker             Invoker
}

func New(options Options) (*Proxy, error) {
	if _, err := gatewayauth.Sign(options.InboundCredentials, gatewayauth.Request{
		Method: http.MethodPost, Path: "/probe", TargetNode: strings.TrimSpace(options.InboundTargetNode),
	}, time.Unix(1, 0)); err != nil {
		return nil, fmt.Errorf("validate inbound credentials: %w", err)
	}
	if _, err := gatewayauth.Sign(options.UpstreamCredentials, gatewayauth.Request{
		Method: http.MethodPost, Path: "/probe", TargetNode: strings.TrimSpace(options.UpstreamTargetNode),
	}, time.Unix(1, 0)); err != nil {
		return nil, fmt.Errorf("validate upstream credentials: %w", err)
	}
	if err := validateTarget(options.UpstreamTarget); err != nil {
		return nil, fmt.Errorf("validate upstream target: %w", err)
	}
	if strings.TrimSpace(options.InboundTargetNode) == "" || strings.TrimSpace(options.UpstreamTargetNode) == "" {
		return nil, errors.New("inbound and upstream target nodes are required")
	}
	maxBodyBytes := options.MaxBodyBytes
	if maxBodyBytes <= 0 {
		maxBodyBytes = defaultMaxBodyBytes
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	nonceNamespace := strings.TrimSpace(options.NonceNamespace)
	if nonceNamespace == "" {
		nonceNamespace = defaultNonceNS
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	invoker := options.Invoker
	if invoker == nil {
		invoker = client.New()
	}
	allowedCallers := make(map[string]struct{}, len(options.AllowedCallers))
	for _, caller := range options.AllowedCallers {
		caller = strings.TrimSpace(caller)
		if caller != "" {
			allowedCallers[caller] = struct{}{}
		}
	}
	return &Proxy{
		inboundCredentials: options.InboundCredentials, upstreamCredentials: options.UpstreamCredentials,
		inboundTargetNode: strings.TrimSpace(options.InboundTargetNode), upstreamTargetNode: strings.TrimSpace(options.UpstreamTargetNode),
		upstreamTarget: strings.TrimSpace(options.UpstreamTarget), allowedCallers: allowedCallers,
		nonces: options.Nonces, nonceNamespace: nonceNamespace, maxBodyBytes: maxBodyBytes, timeout: timeout,
		now: now, invoker: invoker,
	}, nil
}

// Forward transparently proxies the protobuf body while terminating and
// re-signing the Gateway HMAC at the regional Access boundary.
func (p *Proxy) Forward(ctx context.Context, reqbody *codec.Body) (*codec.Body, error) {
	if p == nil {
		return nil, errors.New("storage access proxy is nil")
	}
	if reqbody == nil {
		return nil, errors.New("storage access request body is required")
	}
	msg := codec.Message(ctx)
	if msg == nil {
		return nil, errors.New("storage access tRPC message is missing")
	}
	servicePath, method, ok := splitRPCName(msg.ServerRPCName())
	if !ok {
		return nil, fmt.Errorf("storage access RPC name is invalid: %q", msg.ServerRPCName())
	}
	if !methodAllowed(servicePath, method) {
		return nil, fmt.Errorf("storage access method is not allowed: %s/%s", servicePath, method)
	}
	if int64(len(reqbody.Data)) > p.maxBodyBytes {
		return nil, fmt.Errorf("storage access request body exceeds %d bytes", p.maxBodyBytes)
	}
	metadata := metadataToHeader(msg.ServerMetaData())
	claims, err := gatewayauth.Verify(p.inboundCredentials, gatewayauth.Request{
		Method: http.MethodPost, Path: "/" + strings.TrimPrefix(msg.ServerRPCName(), "/"), TargetNode: p.inboundTargetNode,
		Callee: servicePath, Func: method, Body: reqbody.Data,
	}, metadata, p.now())
	if err != nil {
		return nil, fmt.Errorf("storage access inbound authentication failed: %w", err)
	}
	if len(p.allowedCallers) > 0 {
		if _, ok := p.allowedCallers[claims.Caller]; !ok {
			return nil, fmt.Errorf("storage access caller is not allowed: %s", claims.Caller)
		}
	}
	if p.nonces != nil {
		consumed, err := p.nonces.Consume(ctx, p.nonceNamespace, claims.Nonce, claims.TTL)
		if err != nil {
			return nil, fmt.Errorf("consume storage access nonce: %w", err)
		}
		if !consumed {
			return nil, errors.New("storage access request replayed")
		}
	}

	upstreamCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	upstreamCtx, upstreamMsg := codec.WithCloneMessage(upstreamCtx)
	upstreamMsg.WithClientRPCName("/" + servicePath + "/" + method)
	upstreamMsg.WithCalleeServiceName(servicePath)
	upstreamMsg.WithCalleeMethod(method)

	invokeOptions := []client.Option{
		client.WithTarget(p.upstreamTarget), client.WithNetwork("tcp"), client.WithProtocol("trpc"),
		client.WithServiceName(servicePath), client.WithCalleeMethod(method),
		client.WithSerializationType(msg.SerializationType()), client.WithCurrentSerializationType(codec.SerializationTypeNoop),
		client.WithTimeout(p.timeout), client.WithFilter(rawGatewayClientFilter(p.upstreamCredentials, p.upstreamTargetNode, p.now)),
	}
	for key, value := range msg.ServerMetaData() {
		if strings.HasPrefix(strings.ToLower(key), "x-moox-") {
			continue
		}
		invokeOptions = append(invokeOptions, client.WithMetaData(key, value))
	}
	response := &codec.Body{}
	if err := p.invoker.Invoke(upstreamCtx, reqbody, response, invokeOptions...); err != nil {
		return nil, fmt.Errorf("storage access upstream invoke failed: %w", err)
	}
	if int64(len(response.Data)) > p.maxBodyBytes {
		return nil, fmt.Errorf("storage access response body exceeds %d bytes", p.maxBodyBytes)
	}
	return response, nil
}

func rawGatewayClientFilter(credentials gatewayauth.Credentials, targetNode string, now func() time.Time) filter.ClientFilter {
	if now == nil {
		now = time.Now
	}
	return func(ctx context.Context, req, rsp interface{}, next filter.ClientHandleFunc) error {
		body, ok := req.(*codec.Body)
		if !ok || body == nil {
			return errors.New("storage access upstream request body is invalid")
		}
		msg := codec.Message(ctx)
		if msg == nil {
			return errors.New("storage access upstream tRPC message is missing")
		}
		path := msg.ClientRPCName()
		if path == "" {
			path = "/" + strings.TrimPrefix(msg.CalleeServiceName(), "/") + "/" + msg.CalleeMethod()
		}
		headers, err := gatewayauth.Sign(credentials, gatewayauth.Request{
			Method: http.MethodPost, Path: path, TargetNode: targetNode, Caller: credentials.Caller,
			Callee: msg.CalleeServiceName(), Func: msg.CalleeMethod(), Body: body.Data,
		}, now())
		if err != nil {
			return err
		}
		metadata := make(codec.MetaData, len(msg.ClientMetaData())+len(headers))
		for key, value := range msg.ClientMetaData() {
			metadata[key] = value
		}
		for key, values := range headers {
			if len(values) == 1 {
				metadata[key] = []byte(values[0])
			}
		}
		msg.WithClientMetaData(metadata)
		return next(ctx, req, rsp)
	}
}

func metadataToHeader(metadata codec.MetaData) http.Header {
	headers := make(http.Header, len(metadata))
	for key, value := range metadata {
		headers.Add(key, string(value))
	}
	return headers
}

func methodAllowed(servicePath, method string) bool {
	methods, ok := map[string]map[string]struct{}{
		PrimaryStoreName: {
			"UpsertFields": {}, "ReadFields": {}, "ReadTimeSeriesRows": {}, "ReadRecordRows": {},
			"ReportCollectorPeriodCompleted": {}, "WaitViewSyncPoint": {},
		},
		MetadataName: {
			"RegisterDataSubject": {}, "GetSubject": {}, "ListSubjects": {}, "ListSubjectSymbols": {},
			"GetDataset": {}, "ListDatasets": {}, "CreateDataset": {}, "UpdateDataset": {}, "DeleteDataset": {},
			"CheckDatasetActivation": {}, "ActivateDataset": {}, "ListDatasetSubjects": {},
			"BindDatasetSubject": {}, "StageDatasetSubjectSet": {}, "ActivateDatasetSubjectSet": {}, "UpsertSubject": {},
			"UpsertSubjectSymbol": {}, "UpsertDatasetColumn": {}, "ListDatasetColumns": {},
			"CreateView": {}, "GetView": {}, "ListViews": {}, "UpdateView": {}, "DeleteView": {},
			"UpsertViewColumn": {}, "ListViewColumns": {}, "RequestViewRebuild": {},
		},
		DataViewName: {
			"QueryTimeSeriesRows": {}, "SearchRecordRows": {},
		},
	}[servicePath]
	if !ok {
		return false
	}
	_, ok = methods[method]
	return ok
}

func splitRPCName(rpcName string) (string, string, bool) {
	value := strings.TrimPrefix(strings.TrimSpace(rpcName), "/")
	servicePath, method, ok := strings.Cut(value, "/")
	return servicePath, method, ok && servicePath != "" && method != "" && !strings.Contains(method, "/")
}

func validateTarget(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "ip" || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("target must be an ip://host:port address")
	}
	if _, port, err := net.SplitHostPort(parsed.Host); err != nil || port == "" {
		return errors.New("target must include a port")
	}
	return nil
}

// AccessServiceDesc exposes a wildcard service so the request protobuf is
// never decoded and re-encoded at the regional boundary.
var AccessServiceDesc = server.ServiceDesc{
	ServiceName: AccessServiceName,
	HandlerType: ((*AccessServer)(nil)),
	Methods:     []server.Method{{Name: "*", Func: accessForwardHandler}},
}

type AccessServer interface {
	Forward(context.Context, *codec.Body) (*codec.Body, error)
}

func RegisterAccessService(s server.Service, impl AccessServer) error {
	if s == nil {
		return errors.New("storage access service is unavailable")
	}
	return s.Register(&AccessServiceDesc, impl)
}

func accessForwardHandler(svr interface{}, ctx context.Context, f server.FilterFunc) (interface{}, error) {
	req := &codec.Body{}
	filters, err := f(req)
	if err != nil {
		return nil, err
	}
	handle := func(ctx context.Context, req interface{}) (interface{}, error) {
		body, ok := req.(*codec.Body)
		if !ok {
			return nil, errors.New("storage access request body is invalid")
		}
		return svr.(AccessServer).Forward(ctx, body)
	}
	return filters.Filter(ctx, req, handle)
}
