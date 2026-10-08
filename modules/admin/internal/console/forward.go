package console

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/log"
)

const minGzipForwardResponseBytes = 1024

// setForwardCommonHeaders 设置响应的公共头（CORS，并暴露 trpc 错误头供前端读取）。
func setForwardCommonHeaders(w http.ResponseWriter, origin string) {
	w.Header().Set("Content-Type", "application/json")
	applyCORSHeaders(w, origin)
}

// writeForwardResponse 原样写回目标服务的 JSON 响应。
func writeForwardResponse(w http.ResponseWriter, respBody []byte, headers consoleHeaders) {
	setForwardCommonHeaders(w, headers.origin)
	if headers.traceID != "" {
		w.Header().Set("X-Trace-Id", headers.traceID)
	}
	if shouldGzipForwardResponse(respBody, headers.acceptEncoding) {
		w.Header().Set("Content-Encoding", "gzip")
		addVaryHeader(w, "Accept-Encoding")
		zw := gzip.NewWriter(w)
		_, _ = zw.Write(respBody)
		_ = zw.Close()
		return
	}
	_, _ = w.Write(respBody)
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

// writeForwardError 把 tRPC 错误转写为前端可读的响应：HTTP 200 + trpc-ret（框架码）+ trpc-func-ret（消息），
// 同时写入与业务错误同结构的 JSON（ret_info），避免前端拿到空响应体。
func writeForwardError(ctx context.Context, w http.ResponseWriter, err error, headers consoleHeaders) {
	setForwardCommonHeaders(w, headers.origin)
	if headers.traceID != "" {
		w.Header().Set("X-Trace-Id", headers.traceID)
	}
	code := errs.Code(err)
	msg := errs.Msg(err)
	w.Header().Set("trpc-ret", strconv.Itoa(int(code)))
	if msg != "" {
		// trpc-func-ret 头不允许换行
		w.Header().Set("trpc-func-ret", strings.ReplaceAll(msg, "\n", " "))
	}
	log.WarnContextf(ctx, "控制台转发错误: code=%d msg=%s err=%v", code, msg, err)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(middlewareResp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode(code), Msg: msg}})
}
