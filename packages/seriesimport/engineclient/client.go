// Package engineclient 调用因子引擎的本地接口（挂在健康检查端口上，签名鉴权）：读数据集字段、检查标的范围、查询本地边界。
package engineclient

import (
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
	"github.com/mooyang-code/moox/packages/seriesformat"
)

// Client 是引擎本地接口的客户端。
type Client struct {
	Endpoint    string
	AuthVersion string
	AccessKey   string
	SecretKey   string
	HTTP        *http.Client
}

func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.Endpoint, "/")+path+"?"+query.Encode(), nil)
	if err != nil {
		return err
	}
	if err := c.sign(req); err != nil {
		return err
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: time.Minute}
	}
	rsp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("调用因子引擎：%w", err)
	}
	defer rsp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(rsp.Body, 8<<20))
	if rsp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return fmt.Errorf("因子引擎返回 HTTP %d：%s", rsp.StatusCode, e.Error)
		}
		return fmt.Errorf("因子引擎返回 HTTP %d：%s", rsp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return json.Unmarshal(raw, out)
}

func (c *Client) sign(req *http.Request) error {
	version := c.AuthVersion
	if version == "" {
		version = "moox-health-v1"
	}
	if c.AccessKey == "" || c.SecretKey == "" {
		return errors.New("缺少因子引擎本地接口的鉴权密钥（MOOX_HEALTH_AUTH_ACCESS_KEY / MOOX_HEALTH_AUTH_SECRET_KEY）")
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	ts, n := time.Now().Unix(), hex.EncodeToString(nonce)
	sig, err := requestauth.Sign(c.SecretKey, requestauth.Material{Method: req.Method, Path: req.URL.EscapedPath(), Timestamp: ts, Nonce: n})
	if err != nil {
		return err
	}
	req.Header.Set("X-Moox-Health-Auth", fmt.Sprintf("%s/%s/%d/%s/%s", version, c.AccessKey, ts, n, sig))
	return nil
}

// Fields 返回数据集的字段表。
func (c *Client) Fields(ctx context.Context, space, dataset, freq string) ([]seriesformat.Field, error) {
	var rsp struct {
		Fields []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"fields"`
	}
	if err := c.get(ctx, "/v1/series/fields", url.Values{"space": {space}, "dataset": {dataset}, "freq": {freq}}, &rsp); err != nil {
		return nil, err
	}
	out := make([]seriesformat.Field, 0, len(rsp.Fields))
	for _, f := range rsp.Fields {
		out = append(out, seriesformat.Field{Name: f.Name, Type: seriesformat.FieldType(f.Type)})
	}
	return out, nil
}

// SubjectInScope 判断标的是否在数据集的标签范围内。
func (c *Client) SubjectInScope(ctx context.Context, space, dataset, subject string) (bool, error) {
	var rsp struct {
		InScope bool `json:"in_scope"`
	}
	if err := c.get(ctx, "/v1/series/subject", url.Values{"space": {space}, "dataset": {dataset}, "subject": {subject}}, &rsp); err != nil {
		return false, err
	}
	return rsp.InScope, nil
}

// Bounds 是标的在本地的边界。
type Bounds struct {
	Exists   bool   `json:"exists"`
	Earliest string `json:"earliest"`
	Latest   string `json:"latest"`
}

// Bounds 返回标的在本地时序存储里的最早与最新时间。
func (c *Client) Bounds(ctx context.Context, space, dataset, freq, subject string) (Bounds, error) {
	var out Bounds
	err := c.get(ctx, "/v1/series/bounds", url.Values{"space": {space}, "dataset": {dataset}, "freq": {freq}, "subject": {subject}}, &out)
	return out, err
}
