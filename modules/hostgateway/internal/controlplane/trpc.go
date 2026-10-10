package controlplane

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/rpcconn"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/snapshot"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/tlsconfig"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/security"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/transport"
)

type RPCClient struct {
	cfg                 hostgatewayconfig.Config
	credentials         gatewayauth.Credentials
	instanceID, version string
	pool                *rpcconn.Pool
	transport           transport.ClientTransport
}

func NewRPC(cfg hostgatewayconfig.Config, material *tlsconfig.Material, credentials gatewayauth.Credentials, version string) (*RPCClient, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if material == nil || material.HostID() != cfg.Host.ID || credentials.Caller != cfg.Control.Caller || credentials.KeyID != cfg.Control.KeyID || len(credentials.Secret) < 32 ||
		version == "" || len(version) > 128 || version != strings.TrimSpace(version) || strings.ContainsFunc(version, unicode.IsControl) {
		return nil, errors.New("control client requires matching TLS/signing identity and version")
	}
	instance, err := security.RandomHex(16)
	if err != nil {
		return nil, err
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
	return &RPCClient{cfg: cfg, credentials: credentials, instanceID: instance, version: version, pool: pool, transport: transport.NewClientTransport()}, nil
}

func (c *RPCClient) String() string {
	return "GatewayControlClient{host=" + c.cfg.Host.ID + " instance=" + c.instanceID + "}"
}
func (c *RPCClient) GoString() string   { return c.String() }
func (c *RPCClient) InstanceID() string { return c.instanceID }
func (c *RPCClient) Close() error       { return c.pool.Close() }

// A nil successful result means unchanged; there must already be a valid view.
func (c *RPCClient) Pull(ctx context.Context, currentHash string) (*snapshot.View, error) {
	rsp := &pb.PullSnapshotRsp{}
	if err := c.invoke(ctx, "PullSnapshot", &pb.PullSnapshotReq{HostId: c.cfg.Host.ID, CurrentHash: currentHash}, rsp); err != nil {
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

func (c *RPCClient) Report(ctx context.Context, hash string, count int32, lastError string) error {
	if len(lastError) > 4096 {
		lastError = "host gateway synchronization failed"
	}
	rsp := &pb.ReportStatusRsp{}
	if err := c.invoke(ctx, "ReportStatus", &pb.ReportStatusReq{HostId: c.cfg.Host.ID, InstanceId: c.instanceID, Version: c.version, AppliedHash: hash, RouteCount: count, Error: lastError}, rsp); err != nil {
		return err
	}
	if rsp.RetInfo == nil || rsp.RetInfo.Code != pb.ErrorCode_SUCCESS {
		return errors.New("GatewayControl rejected heartbeat")
	}
	return nil
}

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
	headers, err := gatewayauth.Sign(c.credentials, gatewayauth.Request{Method: "POST", Path: path, TargetNode: c.cfg.Control.HostID, Callee: servicecatalog.GatewayControlPath, Func: method, Body: encoded}, time.Now())
	if err != nil {
		return errors.New("sign GatewayControl request")
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
