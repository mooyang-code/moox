package gateway

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("MOOX_NODE_GATEWAY_URL")), "/")
	secret := strings.TrimSpace(os.Getenv("MOOX_GATEWAY_SERVICE_SECRET_KEY"))
	nodeID := strings.TrimSpace(os.Getenv("MOOX_NODE_GATEWAY_NODE_ID"))
	keyID := strings.TrimSpace(os.Getenv("MOOX_GATEWAY_SERVICE_KEY_ID"))
	if keyID == "" {
		keyID = nodeGatewayServiceKeyID
	}
	if base == "" || secret == "" || nodeID == "" {
		return nil, true, fmt.Errorf("storage BFF requires Node Service Gateway configuration")
	}
	if keyID != nodeGatewayServiceKeyID {
		return nil, true, fmt.Errorf("storage BFF key id %q does not match Node Service Gateway key id %q", keyID, nodeGatewayServiceKeyID)
	}
	if native := strings.TrimSpace(os.Getenv("MOOX_NODE_GATEWAY_NATIVE_URL")); native != "" {
		base = native
	}
	target := strings.TrimPrefix(strings.TrimPrefix(base, "http://"), "https://")
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
	invokeOptions := []client.Option{client.WithTarget("ip://" + target), client.WithNetwork("tcp"), client.WithProtocol("trpc"), client.WithServiceName(servicePath), client.WithCalleeMethod(method), client.WithSerializationType(codec.SerializationTypeNoop), client.WithCurrentSerializationType(codec.SerializationTypeNoop), client.WithTimeout(30 * time.Second)}
	for name, value := range metadata {
		invokeOptions = append(invokeOptions, client.WithMetaData(name, value))
	}
	invokeOptions = append(invokeOptions, client.WithSerializationType(codec.SerializationTypeJSON))
	if err := client.New().Invoke(ctx, &codec.Body{Data: body}, response, invokeOptions...); err != nil {
		return nil, true, err
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(response.Data))}, true, nil
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

// forwardHTTP 把统一网关请求纯透传到目标服务的有协议 http 端口。
// 目标服务由 t_service_deployments 中的 active 部署记录决定：
//   - address: 目标 host:port（本进程 127.0.0.1:port，远端 storage host:port）
//   - path:    trpc 服务全名（如 trpc.moox.infra.Auth）
//
// 请求 URL = /{path}/{method}，框架服务端自动 JSON↔PB，网关不做序列化/加工，
// 原样返回 http body；错误由 trpc 框架以 errs 错误返回，网关转写 trpc-ret/trpc-func-ret header。
func forwardHTTP(ctx context.Context, provider AdminServiceDetailProvider, adminNodeID, serviceID, method string, body []byte, headers map[string]string) ([]byte, error) {
	if GetConfig() == nil {
		return nil, fmt.Errorf("网关配置未初始化")
	}
	detail, err := resolveAdminServiceDetail(ctx, provider, adminNodeID, serviceID)
	if err != nil {
		return nil, err
	}
	return forwardHTTPToDetail(ctx, serviceID, method, detail, body, headers)
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

	opts := []client.Option{
		client.WithTarget(target),
		client.WithCurrentSerializationType(codec.SerializationTypeNoop),
		client.WithDisableServiceRouter(),
		client.WithReqHead(buildForwardHeaders(headers)),
	}
	proxy := thttp.NewClientProxy(serviceID, opts...)
	codecRsp := &codec.Body{}
	if err := proxy.Post(ctx, targetURL, &codec.Body{Data: body}, codecRsp); err != nil {
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
