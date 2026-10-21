// Package target 是导入服务的写入目标。
package target

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
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/requestauth"
	"github.com/mooyang-code/moox/packages/seriesformat"
	"github.com/mooyang-code/moox/packages/seriesimport/runner"
)

// Factor 通过因子引擎的本地写入接口（挂在健康检查端口上，签名鉴权）写入因子机的本地时序存储。
// 本地存储同一时刻只能由引擎进程打开，所以写入只能经过引擎；引擎未就绪时也接受写入。
type Factor struct {
	// Endpoint 是引擎健康检查地址，例如 http://127.0.0.1:11417。
	Endpoint string
	// AuthVersion 为空时取 moox-health-v1。
	AuthVersion string
	AccessKey   string
	SecretKey   string
	Client      *http.Client
}

// Name 实现 runner.Target。
func (f *Factor) Name() string { return "factor" }

// Write 实现 runner.Target：一个批对应一次整段替换。
func (f *Factor) Write(ctx context.Context, c runner.Chunk) error {
	body, err := json.Marshal(wireRequest(c))
	if err != nil {
		return err
	}
	const path = "/v1/series/write"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(f.Endpoint, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := f.sign(req); err != nil {
		return err
	}
	client := f.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	rsp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("调用因子引擎本地写入接口：%w", err)
	}
	defer rsp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(rsp.Body, 1<<20))
	if rsp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return fmt.Errorf("因子引擎拒绝写入（HTTP %d）：%s", rsp.StatusCode, e.Error)
		}
		return fmt.Errorf("因子引擎拒绝写入（HTTP %d）：%s", rsp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil
}

func (f *Factor) sign(req *http.Request) error {
	version := f.AuthVersion
	if version == "" {
		version = "moox-health-v1"
	}
	if f.AccessKey == "" || f.SecretKey == "" {
		return errors.New("缺少因子引擎本地写入接口的鉴权密钥（MOOX_HEALTH_AUTH_ACCESS_KEY / MOOX_HEALTH_AUTH_SECRET_KEY）")
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	ts := time.Now().Unix()
	n := hex.EncodeToString(nonce)
	sig, err := requestauth.Sign(f.SecretKey, requestauth.Material{Method: req.Method, Path: req.URL.EscapedPath(), Timestamp: ts, Nonce: n})
	if err != nil {
		return err
	}
	req.Header.Set("X-Moox-Health-Auth", fmt.Sprintf("%s/%s/%d/%s/%s", version, f.AccessKey, ts, n, sig))
	return nil
}

type wireCol struct {
	Null []bool    `json:"null,omitempty"`
	F    []float64 `json:"f,omitempty"`
	I    []int64   `json:"i,omitempty"`
	S    []string  `json:"s,omitempty"`
}

type writeRequest struct {
	Space         string             `json:"space"`
	Dataset       string             `json:"dataset"`
	Freq          string             `json:"freq"`
	Subject       string             `json:"subject"`
	From          string             `json:"from"`
	To            string             `json:"to"`
	AllowMissing  bool               `json:"allow_missing"`
	RecordPending bool               `json:"record_pending"`
	Reason        string             `json:"reason,omitempty"`
	Times         []int64            `json:"times"`
	Tags          []string           `json:"tags"`
	Cols          map[string]wireCol `json:"cols"`
}

func wireRequest(c runner.Chunk) writeRequest {
	req := writeRequest{
		Space: c.Space, Dataset: c.Dataset, Freq: c.Freq, Subject: c.Subject,
		From: c.From.UTC().Format(time.RFC3339), To: c.To.UTC().Format(time.RFC3339),
		AllowMissing: c.AllowMissing, RecordPending: c.RecordPending, Tags: c.Batch.Tags, Cols: make(map[string]wireCol, len(c.Fields)),
	}
	if c.RecordPending {
		req.Reason = "import"
	}
	req.Times = make([]int64, c.Batch.Len())
	for i, t := range c.Batch.Times {
		req.Times[i] = t.Unix()
	}
	for i, field := range c.Fields {
		col := c.Batch.Cols[i]
		w := wireCol{Null: col.Null}
		switch field.Type {
		case seriesformat.TypeDouble:
			w.F = col.F
		case seriesformat.TypeInt, seriesformat.TypeBool:
			w.I = col.I
		default:
			w.S = col.S
		}
		req.Cols[field.Name] = w
	}
	return req
}
