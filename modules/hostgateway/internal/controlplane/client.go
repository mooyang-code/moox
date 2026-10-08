// Package controlplane 是主机网关访问网关控制（trpc.moox.admin.GatewayControl）的客户端：
//   - control 主机直连 127.0.0.1:11112，自己写入调用方身份；
//   - 其他主机经 control 的 11003（TLS，校验 MooX 私有 CA）访问，由 control 的主机网关校验签名后转发。
package controlplane

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/config"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/gatewayroute"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
)

const requestTimeout = 10 * time.Second

// Status 是一次心跳上报的内容。
type Status struct {
	AppliedHash         string
	RouteCount          int32
	LastError           string
	CertificateNotAfter time.Time
}

// Client 拉取快照并上报心跳。
type Client struct {
	hostID     string
	instanceID string
	version    string
	proxy      adminpb.GatewayControlClientProxy
}

// Options 是客户端参数。
type Options struct {
	Config     config.Config
	InstanceID string
	Version    string
	// extra 供测试注入额外的 tRPC 选项。
	extra []client.Option
}

// New 按配置创建客户端。
func New(options Options) (*Client, error) {
	cfg := options.Config
	if strings.TrimSpace(options.InstanceID) == "" {
		return nil, errors.New("主机网关实例 ID 不能为空")
	}
	clientOptions := []client.Option{
		client.WithTarget("ip://" + cfg.Control.Target), client.WithNetwork("tcp"), client.WithProtocol("trpc"),
		client.WithTimeout(requestTimeout),
	}
	if cfg.Direct() {
		// 直连本机网关控制：没有网关在中间，自己写入可信的调用方身份。
		clientOptions = append(clientOptions, client.WithMetaData(gatewayroute.MetadataVerifiedCaller, []byte(cfg.Control.Caller)))
	} else {
		credentials, err := gatewayauth.LoadCallerKey(cfg.Control.KeyFile)
		if err != nil {
			return nil, err
		}
		if credentials.Caller != cfg.Control.Caller {
			return nil, fmt.Errorf("control.key_file 属于 %s，不是 %s", credentials.Caller, cfg.Control.Caller)
		}
		clientOptions = append(clientOptions,
			client.WithCurrentSerializationType(codec.SerializationTypeNoop),
			client.WithFilter(gatewayauth.NewTRPCClientFilter(credentials, servicecatalog.ControlHostID, nil)),
			client.WithTLS("", "", cfg.TLS.CAFile, servicecatalog.ControlHostID),
			// control 的主机网关重启后，旧的 TLS 连接要能被识别出来，不再复用。
			client.WithPool(gatewayclient.NewConnectionPool()),
		)
	}
	clientOptions = append(clientOptions, options.extra...)
	return &Client{
		hostID: cfg.Host.ID, instanceID: options.InstanceID, version: options.Version,
		proxy: adminpb.NewGatewayControlClientProxy(clientOptions...),
	}, nil
}

// Pull 拉取快照；changed=false 表示与 currentHash 相同。
func (c *Client) Pull(ctx context.Context, currentHash string) (*adminpb.HostSnapshot, bool, error) {
	rsp, err := c.proxy.PullSnapshot(ctx, &adminpb.PullSnapshotReq{HostId: c.hostID, CurrentHash: currentHash})
	if err != nil {
		return nil, false, fmt.Errorf("拉取快照: %w", err)
	}
	if code := rsp.GetRetInfo().GetCode(); code != adminpb.ErrorCode_SUCCESS {
		return nil, false, fmt.Errorf("拉取快照: %s", rsp.GetRetInfo().GetMsg())
	}
	if !rsp.GetChanged() {
		return nil, false, nil
	}
	if rsp.GetSnapshot() == nil {
		return nil, false, errors.New("网关控制报告快照有变化，却没有返回快照")
	}
	return rsp.GetSnapshot(), true, nil
}

// Report 上报心跳。
func (c *Client) Report(ctx context.Context, status Status) error {
	req := &adminpb.ReportStatusReq{
		HostId: c.hostID, InstanceId: c.instanceID, Version: c.version, AppliedHash: status.AppliedHash,
		RouteCount: status.RouteCount, LastError: status.LastError,
	}
	if !status.CertificateNotAfter.IsZero() {
		req.CertificateNotAfter = status.CertificateNotAfter.UTC().Format(time.RFC3339)
	}
	rsp, err := c.proxy.ReportStatus(ctx, req)
	if err != nil {
		return fmt.Errorf("上报心跳: %w", err)
	}
	if code := rsp.GetRetInfo().GetCode(); code != adminpb.ErrorCode_SUCCESS {
		return fmt.Errorf("上报心跳: %s", rsp.GetRetInfo().GetMsg())
	}
	return nil
}
