package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/modules/admin/internal/service/collectmgr/spacecontext"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/log"

	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	thttp "trpc.group/trpc-go/trpc-go/http"
)

// setForwardCommonHeaders 设置透传响应的公共头（CORS + 暴露 trpc 错误头供前端读取）。
func setForwardCommonHeaders(w http.ResponseWriter, origin string) {
	w.Header().Set("Content-Type", "application/json")
	applyCORSHeaders(w, origin)
}

// forwardHTTP 把统一网关请求纯透传到目标服务的有协议 http 端口。
// 目标服务由 gateway.yaml 的 services.{serviceID}.{address,path} 决定：
//   - address: 目标 host:port（本进程 127.0.0.1:port，远端 storage host:port）
//   - path:    trpc 服务全名（如 trpc.moox.infra.Auth）
//
// 请求 URL = /{path}/{method}，框架服务端自动 JSON↔PB，网关不做序列化/加工，
// 原样返回 http body；错误由 trpc 框架以 errs 错误返回，网关转写 trpc-ret/trpc-func-ret header。
func forwardHTTP(ctx context.Context, serviceID, method string, body []byte, headers map[string]string) ([]byte, error) {
	cfg := GetConfig()
	if cfg == nil {
		return nil, fmt.Errorf("网关配置未初始化")
	}
	detail, err := cfg.GetServiceDetail(serviceID)
	if err != nil {
		return nil, err
	}
	if detail.Path == "" || detail.Address == "" {
		return nil, fmt.Errorf("服务 '%s' 配置缺失 address/path", serviceID)
	}
	target := fmt.Sprintf("ip://%s", detail.Address)
	forwardMethod := normalizeForwardMethod(serviceID, method)
	targetURL := fmt.Sprintf("/%s/%s", detail.Path, forwardMethod)
	if forwardMethod != method {
		log.InfoContextf(ctx, "forwardHTTP: %s/%s -> %s (alias %s)", serviceID, method, targetURL, forwardMethod)
	} else {
		log.InfoContextf(ctx, "forwardHTTP: %s/%s -> %s", serviceID, method, targetURL)
	}

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
	return reqHead
}

func addIfPresent(reqHead *thttp.ClientReqHeader, headers map[string]string, key, headerName string) {
	if v, ok := headers[key]; ok && v != "" {
		reqHead.AddHeader(headerName, v)
	}
}

// normalizeForwardMethod 将历史/兼容方法名映射到当前 trpc RPC 方法名。
func normalizeForwardMethod(serviceID, method string) string {
	if serviceID == "cloudnode" && method == "ReportHeartbeatInner" {
		return "ReportHeartbeat"
	}
	return method
}

// writeForwardResponse 写入透传成功响应（原样返回底层 http body，暴露 trpc-ret header 供前端读取）。
func writeForwardResponse(w http.ResponseWriter, respBody []byte, headers map[string]string) {
	setForwardCommonHeaders(w, headers["origin"])
	if traceID := headers["trace_id"]; traceID != "" {
		w.Header().Set("X-Trace-Id", traceID)
	}
	w.Write(respBody)
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
