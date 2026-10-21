package seriesreplay

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/requestauth"
)

// Endpoint 是一个模块的健康检查地址与鉴权密钥（回放接口挂在健康检查端口上，签名鉴权）。
type Endpoint struct {
	URL         string
	AuthVersion string
	AccessKey   string
	SecretKey   string
	HTTP        *http.Client
}

// HTTPError 是模块返回的非 200 响应。
type HTTPError struct {
	Module string
	Status int
	Text   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("%s 返回 HTTP %d：%s", e.Module, e.Status, e.Text)
}

func (e Endpoint) call(ctx context.Context, module, method, path string, query url.Values, body, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	target := strings.TrimRight(e.URL, "/") + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if err := e.sign(req); err != nil {
		return err
	}
	client := e.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	rsp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("调用%s：%w", module, err)
	}
	defer rsp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(rsp.Body, 64<<20))
	if rsp.StatusCode != http.StatusOK {
		text := strings.TrimSpace(string(raw))
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			text = e.Error
		}
		return &HTTPError{Module: module, Status: rsp.StatusCode, Text: text}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func (e Endpoint) sign(req *http.Request) error {
	version := e.AuthVersion
	if version == "" {
		version = "moox-health-v1"
	}
	if e.AccessKey == "" || e.SecretKey == "" {
		return errors.New("缺少模块本地接口的鉴权密钥（MOOX_HEALTH_AUTH_ACCESS_KEY / MOOX_HEALTH_AUTH_SECRET_KEY）")
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	ts, n := time.Now().Unix(), hex.EncodeToString(nonce)
	sig, err := requestauth.Sign(e.SecretKey, requestauth.Material{Method: req.Method, Path: req.URL.EscapedPath(), Timestamp: ts, Nonce: n})
	if err != nil {
		return err
	}
	req.Header.Set("X-Moox-Health-Auth", fmt.Sprintf("%s/%s/%d/%s/%s", version, e.AccessKey, ts, n, sig))
	return nil
}

// FactorAPI 是因子引擎的回放接口。
type FactorAPI interface {
	Open(ctx context.Context, req FactorOpenRequest) (FactorOpenResponse, error)
	Step(ctx context.Context, req FactorStepRequest) (FactorStepResponse, error)
	Close(ctx context.Context, replayID string) error
}

// StrategyAPI 是策略的回放接口。
type StrategyAPI interface {
	Bind(ctx context.Context, instanceID string) (StrategyBinding, error)
	Evaluate(ctx context.Context, req StrategyEvaluateRequest) (StrategyDecision, error)
}

// TradeAPI 是 Trade 纸面账本的回放接口。
type TradeAPI interface {
	CreateSession(ctx context.Context, req TradeCreateRequest) (TradeSession, error)
	GetSession(ctx context.Context, replayID string) (TradeSession, error)
	Advance(ctx context.Context, req TradeAdvanceRequest) (TradeResult, error)
	Periods(ctx context.Context, replayID string, after int64, limit int) ([]TradeResult, error)
	Close(ctx context.Context, replayID string) error
	Delete(ctx context.Context, replayID string) error
}

// FactorClient 是 FactorAPI 的 HTTP 实现。
type FactorClient struct{ Endpoint }

func (c FactorClient) Open(ctx context.Context, req FactorOpenRequest) (FactorOpenResponse, error) {
	var rsp FactorOpenResponse
	return rsp, c.call(ctx, "因子引擎", http.MethodPost, "/v1/replay/open", nil, req, &rsp)
}

func (c FactorClient) Step(ctx context.Context, req FactorStepRequest) (FactorStepResponse, error) {
	var rsp FactorStepResponse
	return rsp, c.call(ctx, "因子引擎", http.MethodPost, "/v1/replay/step", nil, req, &rsp)
}

func (c FactorClient) Close(ctx context.Context, replayID string) error {
	return c.call(ctx, "因子引擎", http.MethodPost, "/v1/replay/close", nil, map[string]string{"replay_id": replayID}, nil)
}

// StrategyClient 是 StrategyAPI 的 HTTP 实现。
type StrategyClient struct{ Endpoint }

func (c StrategyClient) Bind(ctx context.Context, instanceID string) (StrategyBinding, error) {
	var rsp StrategyBinding
	return rsp, c.call(ctx, "策略", http.MethodPost, "/v1/replay/bind", nil, map[string]string{"instance_id": instanceID}, &rsp)
}

func (c StrategyClient) Evaluate(ctx context.Context, req StrategyEvaluateRequest) (StrategyDecision, error) {
	var rsp StrategyDecision
	return rsp, c.call(ctx, "策略", http.MethodPost, "/v1/replay/evaluate", nil, req, &rsp)
}

// TradeClient 是 TradeAPI 的 HTTP 实现。
type TradeClient struct{ Endpoint }

func (c TradeClient) CreateSession(ctx context.Context, req TradeCreateRequest) (TradeSession, error) {
	var rsp TradeSession
	return rsp, c.call(ctx, "Trade", http.MethodPost, "/v1/replay/session", nil, req, &rsp)
}

func (c TradeClient) GetSession(ctx context.Context, replayID string) (TradeSession, error) {
	var rsp TradeSession
	return rsp, c.call(ctx, "Trade", http.MethodGet, "/v1/replay/session", url.Values{"replay_id": {replayID}}, nil, &rsp)
}

func (c TradeClient) Advance(ctx context.Context, req TradeAdvanceRequest) (TradeResult, error) {
	var rsp TradeResult
	return rsp, c.call(ctx, "Trade", http.MethodPost, "/v1/replay/advance", nil, req, &rsp)
}

func (c TradeClient) Periods(ctx context.Context, replayID string, after int64, limit int) ([]TradeResult, error) {
	var rsp struct {
		Periods []TradeResult `json:"periods"`
	}
	query := url.Values{"replay_id": {replayID}, "after": {fmt.Sprint(after)}, "limit": {fmt.Sprint(limit)}}
	err := c.call(ctx, "Trade", http.MethodGet, "/v1/replay/periods", query, nil, &rsp)
	return rsp.Periods, err
}

func (c TradeClient) Close(ctx context.Context, replayID string) error {
	return c.call(ctx, "Trade", http.MethodPost, "/v1/replay/close", nil, map[string]string{"replay_id": replayID}, nil)
}

func (c TradeClient) Delete(ctx context.Context, replayID string) error {
	return c.call(ctx, "Trade", http.MethodPost, "/v1/replay/delete", nil, map[string]string{"replay_id": replayID}, nil)
}
