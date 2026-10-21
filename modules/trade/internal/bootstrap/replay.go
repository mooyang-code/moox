package bootstrap

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	replayapp "github.com/mooyang-code/moox/modules/trade/internal/application/replay"
	"github.com/mooyang-code/moox/modules/trade/internal/health"
	"gorm.io/gorm"
)

// 逐期回放的 Trade 接口（设计 9.2），挂在健康检查端口上（只监听本机，沿用健康检查的签名鉴权）：
//
//	POST /v1/replay/session   建立回放会话（同一个 replay_id 重复建立返回已有会话）；GET 查询会话状态
//	POST /v1/replay/advance   ReplayAdvance：每期必达，同一期重发返回同一条记录
//	GET  /v1/replay/periods   按周期升序返回结果（权益曲线、订单），after 为决策时刻毫秒
//	POST /v1/replay/close     结束会话，保留结果
//	POST /v1/replay/delete    清理一次回放的全部结果
const maxReplayBody = 8 << 20

func replayRoutes(svc *replayapp.Service) []health.Route {
	h := replayHandlers{svc: svc}
	return []health.Route{
		{Path: "/v1/replay/session", Handler: http.HandlerFunc(h.session)},
		{Path: "/v1/replay/advance", Handler: http.HandlerFunc(h.advance)},
		{Path: "/v1/replay/periods", Handler: http.HandlerFunc(h.periods)},
		{Path: "/v1/replay/close", Handler: http.HandlerFunc(h.close)},
		{Path: "/v1/replay/delete", Handler: http.HandlerFunc(h.delete)},
	}
}

type replayHandlers struct{ svc *replayapp.Service }

func decodeBody(w http.ResponseWriter, r *http.Request, out any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxReplayBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		writeReplayError(w, http.StatusBadRequest, err)
		return false
	}
	return true
}

func writeReplayJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeReplayError(w http.ResponseWriter, status int, err error) {
	writeReplayJSON(w, status, map[string]string{"error": err.Error()})
}

func replayStatus(err error) int {
	switch {
	case errors.Is(err, replayapp.ErrInvalidRequest):
		return http.StatusBadRequest
	case errors.Is(err, replayapp.ErrMissingQuote):
		return http.StatusUnprocessableEntity
	case errors.Is(err, replayapp.ErrConflict):
		return http.StatusConflict
	case errors.Is(err, gorm.ErrRecordNotFound):
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

func (h replayHandlers) session(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var req replayapp.CreateRequest
		if !decodeBody(w, r, &req) {
			return
		}
		session, err := h.svc.CreateSession(r.Context(), req)
		if err != nil {
			writeReplayError(w, replayStatus(err), err)
			return
		}
		writeReplayJSON(w, http.StatusOK, session)
	case http.MethodGet:
		session, err := h.svc.GetSession(r.Context(), r.URL.Query().Get("replay_id"))
		if err != nil {
			writeReplayError(w, replayStatus(err), err)
			return
		}
		writeReplayJSON(w, http.StatusOK, session)
	default:
		writeReplayError(w, http.StatusMethodNotAllowed, errors.New("POST or GET only"))
	}
}

func (h replayHandlers) advance(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeReplayError(w, http.StatusMethodNotAllowed, errors.New("POST only"))
		return
	}
	var req replayapp.AdvanceRequest
	if !decodeBody(w, r, &req) {
		return
	}
	result, err := h.svc.Advance(r.Context(), req)
	if err != nil {
		writeReplayError(w, replayStatus(err), err)
		return
	}
	writeReplayJSON(w, http.StatusOK, result)
}

func (h replayHandlers) periods(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeReplayError(w, http.StatusMethodNotAllowed, errors.New("GET only"))
		return
	}
	query := r.URL.Query()
	after, _ := strconv.ParseInt(query.Get("after"), 10, 64)
	limit, _ := strconv.Atoi(query.Get("limit"))
	periods, err := h.svc.Periods(r.Context(), query.Get("replay_id"), after, limit)
	if err != nil {
		writeReplayError(w, replayStatus(err), err)
		return
	}
	writeReplayJSON(w, http.StatusOK, map[string]any{"periods": periods})
}

type replayIDRequest struct {
	ReplayID string `json:"replay_id"`
}

func (h replayHandlers) close(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeReplayError(w, http.StatusMethodNotAllowed, errors.New("POST only"))
		return
	}
	var req replayIDRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if err := h.svc.Close(r.Context(), req.ReplayID); err != nil {
		writeReplayError(w, replayStatus(err), err)
		return
	}
	writeReplayJSON(w, http.StatusOK, map[string]bool{"closed": true})
}

func (h replayHandlers) delete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeReplayError(w, http.StatusMethodNotAllowed, errors.New("POST only"))
		return
	}
	var req replayIDRequest
	if !decodeBody(w, r, &req) {
		return
	}
	deleted, err := h.svc.Delete(r.Context(), req.ReplayID)
	if err != nil {
		writeReplayError(w, replayStatus(err), err)
		return
	}
	writeReplayJSON(w, http.StatusOK, map[string]bool{"deleted": deleted})
}
