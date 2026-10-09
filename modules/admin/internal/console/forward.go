package console

import (
	"compress/gzip"
	"context"
	"encoding/json"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"net/http"
	"strconv"
	"strings"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/log"
)

const minGzipForwardResponseBytes = 1024

// setForwardCommonHeaders 设置透传响应的公共头（CORS + 暴露 trpc 错误头供前端读取）。
func setForwardCommonHeaders(w http.ResponseWriter, origin string) {
	w.Header().Set("Content-Type", "application/json")
	applyCORSHeaders(w, origin)
}

// writeForwardResponse 写入透传成功响应（原样返回 JSON body，暴露 trpc-ret header 供前端读取）。
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
	log.WarnContextf(ctx, "console RPC 错误: code=%d msg=%s err=%v", code, msg, err)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(middlewareResp{
		RetInfo: &pb.RetInfo{
			Code: pb.ErrorCode(code),
			Msg:  msg,
		},
	})
}
