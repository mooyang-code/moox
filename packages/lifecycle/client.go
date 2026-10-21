package lifecycle

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
)

// Client 调用模块的生命周期接口（挂在健康检查端口上，签名鉴权）。
type Client struct {
	Endpoint    string
	AuthVersion string
	AccessKey   string
	SecretKey   string
	HTTP        *http.Client
}

// Status 读取模块的状态快照。
func (c *Client) Status(ctx context.Context) (Snapshot, error) {
	var snap Snapshot
	err := c.do(ctx, http.MethodGet, StatusPath, nil, &snap)
	return snap, err
}

// Do 执行一个命令并返回对象的新状态。
func (c *Client) Do(ctx context.Context, cmd Command, ref Ref) (Object, error) {
	var obj Object
	err := c.do(ctx, http.MethodPost, CommandPath, CommandRequest{Command: cmd, Kind: ref.Kind, ID: ref.ID}, &obj)
	return obj, err
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Endpoint, "/")+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if err := c.sign(req); err != nil {
		return err
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	rsp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("调用模块生命周期接口：%w", err)
	}
	defer rsp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(rsp.Body, 8<<20))
	if rsp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return fmt.Errorf("模块返回 HTTP %d：%s", rsp.StatusCode, e.Error)
		}
		return fmt.Errorf("模块返回 HTTP %d：%s", rsp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return json.Unmarshal(raw, out)
}

func (c *Client) sign(req *http.Request) error {
	version := c.AuthVersion
	if version == "" {
		version = "moox-health-v1"
	}
	if c.AccessKey == "" || c.SecretKey == "" {
		return errors.New("缺少模块本地接口的鉴权密钥（MOOX_HEALTH_AUTH_ACCESS_KEY / MOOX_HEALTH_AUTH_SECRET_KEY）")
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
