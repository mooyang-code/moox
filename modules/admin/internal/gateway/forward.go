package gateway

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/spacecontext"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/log"

	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	thttp "trpc.group/trpc-go/trpc-go/http"
)

const minGzipForwardResponseBytes = 1024
const nodeGatewayServiceKeyID = "moox-gateway-service"
const maxForwardResponseBytes = 16 << 20

// setForwardCommonHeaders 设置透传响应的公共头（CORS + 暴露 trpc 错误头供前端读取）。
func setForwardCommonHeaders(w http.ResponseWriter, origin string) {
	w.Header().Set("Content-Type", "application/json")
	applyCORSHeaders(w, origin)
}

// forwardStorageToNodeGateway keeps the browser Storage facade behind the
// node gateway when the deployment supplies its service-gateway credentials.
// The direct deployment detail path remains available only for local tests and
// non-storage admin APIs.
func forwardStorageToNodeGateway(ctx context.Context, serviceID, method string, body []byte, headers map[string]string) (*http.Response, bool, error) {
	native := strings.TrimSpace(os.Getenv("MOOX_NODE_GATEWAY_NATIVE_URL"))
	secret := strings.TrimSpace(os.Getenv("MOOX_GATEWAY_SERVICE_SECRET_KEY"))
	nodeID := strings.TrimSpace(os.Getenv("MOOX_NODE_GATEWAY_NODE_ID"))
	keyID := strings.TrimSpace(os.Getenv("MOOX_GATEWAY_SERVICE_KEY_ID"))
	if keyID == "" {
		keyID = nodeGatewayServiceKeyID
	}
	if native == "" || secret == "" || nodeID == "" {
		return nil, true, fmt.Errorf("storage BFF requires Node Service Gateway configuration (native target, credentials, and node id)")
	}
	if keyID != nodeGatewayServiceKeyID {
		return nil, true, fmt.Errorf("storage BFF key id %q does not match Node Service Gateway key id %q", keyID, nodeGatewayServiceKeyID)
	}
	target, err := normalizeNodeGatewayTarget(native)
	if err != nil {
		return nil, true, err
	}
	servicePath := storageBFFServicePath(serviceID, method)
	path := "/" + servicePath + "/" + method
	signed, err := gatewayauth.Sign(gatewayauth.Credentials{KeyID: keyID, Secret: secret}, gatewayauth.Request{
		Caller: "admin-gateway", Method: http.MethodPost, Path: path, TargetNode: nodeID, Callee: servicePath, Func: method, Body: body,
	}, time.Now())
	if err != nil {
		return nil, true, err
	}
	metadata := make(codec.MetaData, len(signed))
	for name, values := range signed {
		if len(values) == 1 {
			metadata[name] = []byte(values[0])
		}
	}
	for key, name := range map[string]string{"trace_id": "X-Trace-Id", "space_id": "X-Space-Id", "user_id": "X-User-Id", "user_role": "X-User-Role"} {
		if value := headers[key]; value != "" {
			metadata[name] = []byte(value)
		}
	}
	response := &codec.Body{}
	codec.Message(ctx).WithClientRPCName(path)
	invokeOptions := []client.Option{client.WithTarget(target), client.WithNetwork("tcp"), client.WithProtocol("trpc"), client.WithServiceName(servicePath), client.WithCalleeMethod(method), client.WithSerializationType(codec.SerializationTypeNoop), client.WithCurrentSerializationType(codec.SerializationTypeNoop), client.WithTimeout(30 * time.Second)}
	for name, value := range metadata {
		invokeOptions = append(invokeOptions, client.WithMetaData(name, value))
	}
	invokeOptions = append(invokeOptions, client.WithSerializationType(codec.SerializationTypeJSON))
	if err := client.New().Invoke(ctx, &codec.Body{Data: body}, response, invokeOptions...); err != nil {
		return nil, true, fmt.Errorf("Node Service Gateway %s unavailable: %w", target, err)
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(response.Data))}, true, nil
}

// forwardTradeConsoleToGateway keeps a dedicated TradeConsole listener
// loopback-only while allowing the Admin browser BFF to reach it through the
// authenticated HTTPS Node Gateway. The gateway ACL, not the browser-facing
// Admin route, remains the authority for permitted TradeConsole methods.
func forwardTradeConsoleToGateway(ctx context.Context, method string, detail ServiceDetail, body []byte, headers map[string]string) ([]byte, error) {
	if method == "" || strings.IndexFunc(method, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-')
	}) >= 0 {
		return nil, fmt.Errorf("Trade method is invalid")
	}
	base := strings.TrimRight(strings.TrimSpace(detail.GatewayURL), "/")
	if base == "" || strings.TrimSpace(detail.GatewayNode) == "" {
		return nil, fmt.Errorf("Trade Gateway placement is incomplete")
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return nil, fmt.Errorf("Trade Gateway URL is invalid")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && scheme != "http" {
		return nil, fmt.Errorf("Trade Gateway URL is invalid")
	}
	if scheme == "http" {
		hostname := strings.TrimSpace(parsed.Hostname())
		if hostname != "localhost" {
			ip := net.ParseIP(hostname)
			if ip == nil || !ip.IsLoopback() {
				return nil, fmt.Errorf("Trade Gateway URL must use HTTPS")
			}
		}
	}
	path := "/api/service/trade_console/" + method
	secret := strings.TrimSpace(os.Getenv("MOOX_GATEWAY_SERVICE_SECRET_KEY"))
	keyID := strings.TrimSpace(os.Getenv("MOOX_GATEWAY_SERVICE_KEY_ID"))
	if secret == "" || keyID == "" {
		return nil, fmt.Errorf("Trade Gateway credentials are not configured")
	}
	signed, err := gatewayauth.Sign(gatewayauth.Credentials{KeyID: keyID, Caller: "admin-gateway", Secret: secret}, gatewayauth.Request{
		Method: http.MethodPost, Path: path, TargetNode: detail.GatewayNode, Body: body,
	}, time.Now())
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	for name, values := range signed {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	for key, name := range map[string]string{"space_id": "X-Space-Id", "trace_id": "X-Trace-Id", "user_id": "X-User-Id", "user_role": "X-User-Role"} {
		if value := headers[key]; value != "" {
			request.Header.Set(name, value)
		}
	}
	caFile := strings.TrimSpace(os.Getenv("MOOX_TRADE_GATEWAY_CA_FILE"))
	if caFile == "" {
		caFile = strings.TrimSpace(os.Getenv("MOOX_GATEWAY_CA_FILE"))
	}
	httpClient, err := gatewayauth.NewHTTPClient(gatewayauth.ClientOptions{Timeout: 30 * time.Second, CAFile: caFile})
	if err != nil {
		return nil, err
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("Trade Gateway unavailable: %w", err)
	}
	defer response.Body.Close()
	encoded, err := io.ReadAll(io.LimitReader(response.Body, maxForwardResponseBytes+1))
	if err != nil || len(encoded) > maxForwardResponseBytes {
		return nil, fmt.Errorf("Trade Gateway response is invalid")
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Trade Gateway returned HTTP %d", response.StatusCode)
	}
	return encoded, nil
}

// normalizeNodeGatewayTarget accepts the deployment formats used by the
// other native tRPC clients. In particular, do not prepend ip:// twice when
// the environment already contains a native tRPC target.
func normalizeNodeGatewayTarget(raw string) (string, error) {
	target := strings.TrimRight(strings.TrimSpace(raw), "/")
	if target == "" {
		return "", fmt.Errorf("Node Service Gateway target is empty")
	}
	if strings.HasPrefix(target, "ip://") {
		return target, nil
	}
	if strings.HasPrefix(target, "http://") {
		return "ip://" + strings.TrimPrefix(target, "http://"), nil
	}
	if strings.HasPrefix(target, "https://") {
		return "ip://" + strings.TrimPrefix(target, "https://"), nil
	}
	if strings.Contains(target, "://") {
		return "", fmt.Errorf("unsupported Node Service Gateway target %q", raw)
	}
	return "ip://" + target, nil
}

func writeNodeGatewayResponse(w http.ResponseWriter, response *http.Response, headers map[string]string) {
	defer response.Body.Close()
	setForwardCommonHeaders(w, headers["origin"])
	for name, values := range response.Header {
		if strings.EqualFold(name, "Content-Length") || strings.EqualFold(name, "Content-Encoding") {
			continue
		}
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

func forwardHTTPToDetail(ctx context.Context, serviceID, method string, detail ServiceDetail, body []byte, headers map[string]string) ([]byte, error) {
	cfg := GetConfig()
	if cfg == nil {
		return nil, fmt.Errorf("网关配置未初始化")
	}
	if detail.Path == "" || detail.Address == "" {
		return nil, fmt.Errorf("服务 '%s' 配置缺失 address/path", serviceID)
	}
	target := fmt.Sprintf("ip://%s", detail.Address)
	targetURL := fmt.Sprintf("/%s/%s", detail.Path, method)
	log.InfoContextf(ctx, "forwardHTTP: %s/%s -> %s", serviceID, method, targetURL)
	timeout := detail.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	opts := []client.Option{
		client.WithTarget(target),
		client.WithCurrentSerializationType(codec.SerializationTypeNoop),
		client.WithDisableServiceRouter(),
		client.WithReqHead(buildForwardHeaders(headers)),
		client.WithTimeout(timeout),
	}
	proxy := thttp.NewClientProxy(serviceID, opts...)
	codecRsp := &codec.Body{}
	forwardCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := proxy.Post(forwardCtx, targetURL, &codec.Body{Data: body}, codecRsp); err != nil {
		return nil, err
	}
	return codecRsp.Data, nil
}

// buildForwardHeaders 构建透传到底层服务的 HTTP 请求头（space_id/trace_id/client_ip/user_agent/access_token）。
func buildForwardHeaders(headers map[string]string) *thttp.ClientReqHeader {
	reqHead := &thttp.ClientReqHeader{}
	reqHead.AddHeader("Content-Type", "application/json;charset=utf-8")
	addIfPresent(reqHead, headers, "client_ip", "X-Client-Ip")
	addIfPresent(reqHead, headers, "trace_id", "X-Trace-Id")
	addIfPresent(reqHead, headers, "user_agent", "User-Agent")
	addIfPresent(reqHead, headers, "access_token", "X-Access-Token")
	addIfPresent(reqHead, headers, "space_id", spacecontext.SpaceIDHeader)
	addIfPresent(reqHead, headers, "user_id", "X-User-Id")
	addIfPresent(reqHead, headers, "user_role", "X-User-Role")
	return reqHead
}

func addIfPresent(reqHead *thttp.ClientReqHeader, headers map[string]string, key, headerName string) {
	if v, ok := headers[key]; ok && v != "" {
		reqHead.AddHeader(headerName, v)
	}
}

// writeForwardResponse 写入透传成功响应（原样返回底层 http body，暴露 trpc-ret header 供前端读取）。
func writeForwardResponse(w http.ResponseWriter, respBody []byte, headers map[string]string) {
	setForwardCommonHeaders(w, headers["origin"])
	if traceID := headers["trace_id"]; traceID != "" {
		w.Header().Set("X-Trace-Id", traceID)
	}
	if shouldGzipForwardResponse(respBody, headers["accept_encoding"]) {
		w.Header().Set("Content-Encoding", "gzip")
		addVaryHeader(w, "Accept-Encoding")
		zw := gzip.NewWriter(w)
		_, _ = zw.Write(respBody)
		_ = zw.Close()
		return
	}
	w.Write(respBody)
}

func shouldGzipForwardResponse(respBody []byte, acceptEncoding string) bool {
	if len(respBody) < minGzipForwardResponseBytes {
		return false
	}
	for _, encoding := range strings.Split(acceptEncoding, ",") {
		if strings.EqualFold(strings.TrimSpace(strings.SplitN(encoding, ";", 2)[0]), "gzip") {
			return true
		}
	}
	return false
}

func addVaryHeader(w http.ResponseWriter, value string) {
	current := w.Header().Get("Vary")
	if current == "" {
		w.Header().Set("Vary", value)
		return
	}
	for _, part := range strings.Split(current, ",") {
		if strings.EqualFold(strings.TrimSpace(part), value) {
			return
		}
	}
	w.Header().Set("Vary", current+", "+value)
}

// writeForwardError 把 trpc 框架错误转写为前端可读的响应。
// 与 trpc-go 有协议 http 服务端错误协议一致：HTTP 200 + trpc-ret(框架码) + trpc-func-ret(业务码)，
// 同时写入与业务错误同结构的 JSON body（ret_info），避免前端拿到空 body 无法识别错误。
func writeForwardError(ctx context.Context, w http.ResponseWriter, err error, headers map[string]string) {
	setForwardCommonHeaders(w, headers["origin"])
	if traceID := headers["trace_id"]; traceID != "" {
		w.Header().Set("X-Trace-Id", traceID)
	}
	code := errs.Code(err)
	msg := errs.Msg(err)
	w.Header().Set("trpc-ret", strconv.Itoa(int(code)))
	if msg != "" {
		// trpc-func-ret 头不允许换行，扁平化
		w.Header().Set("trpc-func-ret", strings.ReplaceAll(msg, "\n", " "))
	}
	log.WarnContextf(ctx, "forwardHTTP 错误: code=%d msg=%s err=%v", code, msg, err)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(middlewareResp{
		RetInfo: &pb.RetInfo{
			Code: pb.ErrorCode(code),
			Msg:  msg,
		},
	})
}
