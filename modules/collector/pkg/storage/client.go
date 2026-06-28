package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-go/log"
)

const (
	registerDataSubjectPath = "/trpc.moox.storage.Metadata/RegisterDataSubject"
	writeTimeSeriesRowsPath = "/trpc.moox.storage.Access/WriteTimeSeriesRows"
	writeRecordRowsPath     = "/trpc.moox.storage.Access/WriteRecordRows"
	defaultClientTimeout    = 8 * time.Second
)

// Client is a small JSON-over-HTTP client for storage metadata/access APIs.
type Client struct {
	baseURL    string
	authInfo   AuthInfo
	httpClient *http.Client
}

func NewClient(baseURL string, authInfo AuthInfo) *Client {
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		authInfo: authInfo,
		httpClient: &http.Client{
			Timeout: defaultClientTimeout,
		},
	}
}

func (c *Client) RegisterDataSubject(ctx context.Context, req RegisterDataSubjectRequest) error {
	req.AuthInfo = c.authInfo
	return c.post(ctx, registerDataSubjectPath, req)
}

func (c *Client) WriteTimeSeriesRows(ctx context.Context, rows []TimeSeriesRow) error {
	return c.post(ctx, writeTimeSeriesRowsPath, WriteTimeSeriesRowsRequest{
		AuthInfo: c.authInfo,
		Rows:     rows,
	})
}

func (c *Client) WriteRecordRows(ctx context.Context, rows []RecordRow) error {
	return c.post(ctx, writeRecordRowsPath, WriteRecordRowsRequest{
		AuthInfo: c.authInfo,
		Rows:     rows,
	})
}

func (c *Client) post(ctx context.Context, path string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("序列化失败: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	log.DebugContextf(ctx, "[storage-client] POST start url=%s path=%s timeout=%s bytes=%d", c.baseURL, path, c.httpClient.Timeout, len(data))
	resp, err := c.httpClient.Do(req)
	if err != nil {
		log.WarnContextf(ctx, "[storage-client] POST error url=%s path=%s duration=%s error=%v", c.baseURL, path, time.Since(start), err)
		return fmt.Errorf("发送请求失败: %w", err)
	}
	defer resp.Body.Close()

	var respBody bytes.Buffer
	_, _ = respBody.ReadFrom(resp.Body)
	log.DebugContextf(ctx, "[storage-client] POST response url=%s path=%s status=%d duration=%s bytes=%d", c.baseURL, path, resp.StatusCode, time.Since(start), respBody.Len())
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, respBody.String())
	}

	var out struct {
		RetInfo RetInfo `json:"ret_info"`
	}
	if err := json.Unmarshal(respBody.Bytes(), &out); err != nil {
		return fmt.Errorf("解析响应失败: %w", err)
	}
	log.DebugContextf(ctx, "[storage-client] POST ret_info url=%s path=%s code=%d msg=%s", c.baseURL, path, out.RetInfo.Code, out.RetInfo.Msg)
	if out.RetInfo.Code != 0 {
		return fmt.Errorf("错误码 %d: %s", out.RetInfo.Code, out.RetInfo.Msg)
	}
	return nil
}
