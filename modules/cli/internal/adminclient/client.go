// Package adminclient 是 moox-cli 访问控制面服务（采集任务、云节点、发布租约、密钥）的客户端。
// 调用经 gatewayclient 以 moox-cli 身份发出，请求和响应都用 JSON 序列化。
package adminclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/gatewayroute"
	"trpc.group/trpc-go/trpc-go/codec"
)

// 控制面服务的 tRPC 服务名。
const (
	ServiceCollectMgr   = "trpc.moox.collector.CollectMgr"
	ServiceCloudNodeMgr = "trpc.moox.cloudnode.CloudNodeMgr"
	ServiceFactorMgr    = "trpc.moox.factor.FactorMgr"
	ServicePublishLease = "trpc.moox.admin.CollectorPublishLease"
	ServiceSecretMgr    = "trpc.moox.ops.SecretMgr"
)

// Sender 发送一次 JSON 序列化的调用并返回响应体；spaceID 非空时经 tRPC 元数据 x-space-id 传给目标服务。
type Sender func(ctx context.Context, servicePath, method string, body []byte, spaceID string) ([]byte, error)

// Client calls the MooX control-plane services used by CLI workflows.
type Client struct {
	send Sender
	// SpaceID 是请求所属的空间，经元数据 x-space-id 传给目标服务。
	SpaceID        string
	publishLeaseMu sync.RWMutex
	publishLease   *CollectorPublishLease
}

// New 经 gatewayclient 发送调用；timeout 大于 0 时作为单次调用的超时。
func New(gateway *gatewayclient.Client, timeout time.Duration) *Client {
	return NewWithSender(func(ctx context.Context, servicePath, method string, body []byte, spaceID string) ([]byte, error) {
		var opts []gatewayclient.CallOption
		if timeout > 0 {
			opts = append(opts, gatewayclient.WithTimeout(timeout))
		}
		if spaceID != "" {
			opts = append(opts, gatewayclient.WithMetadata(gatewayroute.MetadataSpaceID, []byte(spaceID)))
		}
		return gateway.Forward(ctx, servicePath, method, codec.SerializationTypeJSON, body, opts...)
	})
}

// NewWithSender 用给定的发送函数创建客户端；测试替身见 admintest 包。
func NewWithSender(send Sender) *Client {
	return &Client{send: send}
}

type retInfo struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

func isRetInfoSuccess(code int) bool {
	return code == 0 || code == 200
}

func isRetInfoNotFound(info retInfo) bool {
	if info.Code == 404 {
		return true
	}
	message := strings.ToLower(strings.TrimSpace(info.Msg))
	return strings.Contains(message, "not found") || strings.Contains(message, "record not found")
}

// call 以 JSON 序列化调用 servicePath/method，返回原始响应体。
func (c *Client) call(ctx context.Context, servicePath, method string, body any) ([]byte, error) {
	if c == nil || c.send == nil {
		return nil, errors.New("控制面客户端未配置")
	}
	raw := []byte("{}")
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		raw = data
	}
	out, err := c.send(ctx, servicePath, method, raw, strings.TrimSpace(c.SpaceID))
	if err != nil {
		return nil, fmt.Errorf("调用 %s/%s: %w", servicePath, method, err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s/%s 返回了空响应", servicePath, method)
	}
	return out, nil
}

// CallJSON 调用 servicePath/method 并把 JSON 响应解码到 response；用于本包没有封装的方法（例如 FactorMgr）。
func (c *Client) CallJSON(ctx context.Context, servicePath, method string, body, response any) error {
	raw, err := c.call(ctx, servicePath, method, body)
	if err != nil {
		return err
	}
	if response == nil {
		return nil
	}
	if err := json.Unmarshal(raw, response); err != nil {
		return fmt.Errorf("解析 %s/%s 响应: %w", servicePath, method, err)
	}
	return nil
}
