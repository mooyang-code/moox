package controlplane

import (
	"context"
	"errors"
	"net/http"
	"time"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/rpcconn"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/snapshot"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/tlsconfig"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/transport"
)

type RPCClient struct {
	cfg         hostgatewayconfig.Config
	credentials gatewayauth.Credentials
	pool        *rpcconn.Pool
	transport   transport.ClientTransport
	relayStatus func() []*pb.RelaySessionStatus
}

func NewRPC(cfg hostgatewayconfig.Config, material *tlsconfig.Material, credentials gatewayauth.Credentials) (*RPCClient, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if material == nil || material.HostID() != cfg.Host.ID || credentials.Caller != "host-gateway" || credentials.Host != cfg.Host.ID || credentials.PublicKey() == nil {
		return nil, errors.New("control client requires matching TLS/signing identity")
	}
	var pool *rpcconn.Pool
	if cfg.Host.ID == cfg.Control.HostID {
		pool = rpcconn.New(nil)
	} else {
		trust, err := material.Client(cfg.Control.HostID)
		if err != nil {
			return nil, err
		}
		pool = rpcconn.New(trust)
	}
	return &RPCClient{cfg: cfg, credentials: credentials, pool: pool, transport: transport.NewClientTransport()}, nil
}

func (c *RPCClient) String() string {
	return "GatewayControlClient{host=" + c.cfg.Host.ID + "}"
}
func (c *RPCClient) GoString() string { return c.String() }
func (c *RPCClient) Close() error     { return c.pool.Close() }

// Pull 拉取快照，同时把已应用的快照哈希（currentHash）带给 Admin：拉取兼作心跳。
// A nil successful result means unchanged; there must already be a valid view.
func (c *RPCClient) Pull(ctx context.Context, currentHash string) (*snapshot.View, error) {
	req := &pb.PullSnapshotReq{HostId: c.cfg.Host.ID, CurrentHash: currentHash}
	if c.relayStatus != nil {
		req.RelaySessions = c.relayStatus()
	}
	rsp := &pb.PullSnapshotRsp{}
	if err := c.invoke(ctx, "PullSnapshot", req, rsp); err != nil {
		return nil, err
	}
	if rsp.RetInfo == nil || rsp.RetInfo.Code != pb.ErrorCode_SUCCESS {
		return nil, errors.New("GatewayControl rejected snapshot request")
	}
	if !rsp.Changed {
		if currentHash == "" || rsp.Snapshot != nil {
			return nil, snapshot.ErrInvalid
		}
		return nil, nil
	}
	if rsp.Snapshot == nil || rsp.Snapshot.Hash == currentHash {
		return nil, snapshot.ErrInvalid
	}
	return snapshot.Build(c.cfg.Host.ID, rsp.Snapshot)
}

// SetRelayStatus 让每次拉取带上反向连接会话状态，只在 control 网关上设置，须在开始同步前调用。
func (c *RPCClient) SetRelayStatus(status func() []*pb.RelaySessionStatus) { c.relayStatus = status }

func (c *RPCClient) invoke(parent context.Context, method string, req, rsp proto.Message) error {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	encoded, err := proto.Marshal(req)
	if err != nil {
		return errors.New("encode GatewayControl request")
	}
	path := "/" + servicecatalog.GatewayControlPath + "/" + method
	// 控制机网关直连本机的 Admin（回环端口，不验签），自己带上签名主机头；其余主机经控制机网关，由它验签后写这个头。
	headers := http.Header{gatewayauth.HeaderVerifiedHost: {c.cfg.Host.ID}}
	if c.cfg.Host.ID != c.cfg.Control.HostID {
		if headers, err = gatewayauth.Sign(c.credentials, gatewayauth.Request{TargetNode: c.cfg.Control.HostID, Callee: servicecatalog.GatewayControlPath, Func: method, Body: encoded}, time.Now()); err != nil {
			return errors.New("sign GatewayControl request")
		}
	}
	ctx, message := codec.WithNewMessage(ctx)
	defer codec.PutBackMessage(message)
	message.WithClientRPCName(path)
	remaining := 5 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		remaining = time.Until(deadline)
	}
	options := []client.Option{
		client.WithTarget("ip://" + c.cfg.Control.Target), client.WithNetwork("tcp"), client.WithProtocol("trpc"),
		client.WithServiceName(servicecatalog.GatewayControlPath), client.WithCalleeMethod(method),
		client.WithSerializationType(codec.SerializationTypePB), client.WithCurrentSerializationType(codec.SerializationTypeNoop),
		client.WithTransport(c.transport), client.WithPool(c.pool), client.WithMultiplexed(false),
		client.WithTimeout(remaining), client.WithDialTimeout(remaining),
	}
	for key, values := range headers {
		options = append(options, client.WithMetaData(key, []byte(values[0])))
	}
	raw := &codec.Body{}
	if err := client.DefaultClient.Invoke(ctx, &codec.Body{Data: encoded}, raw, options...); err != nil {
		return err
	}
	if len(raw.Data) > snapshot.MaxBytes || proto.Unmarshal(raw.Data, rsp) != nil {
		return errors.New("invalid GatewayControl response encoding or size")
	}
	return nil
}
