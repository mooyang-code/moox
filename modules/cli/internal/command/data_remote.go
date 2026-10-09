package command

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/gatewayroute"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// storageCallTimeout 是 cli 调用 Storage 的单次超时。
const storageCallTimeout = 60 * time.Second

// storageInvoker 是 cli 调用 Storage 的方式：生产经 SSH 隧道的 gatewayclient（moox-cli 身份），测试用替身。
type storageInvoker interface {
	Invoke(ctx context.Context, servicePath, method string, req, rsp any, opts ...gatewayclient.CallOption) error
}

// openStorageInvoker 以 moox-cli 身份经 SSH 隧道访问 Storage；manifestFile 为空时读当前目录的 moox.toml。
func openStorageInvoker(manifestFile string) (storageInvoker, func(), error) {
	gateway, err := openControlGatewayFromFile(manifestFile)
	if err != nil {
		return nil, nil, err
	}
	return gateway, gateway.Close, nil
}

// retInfoResponse 定义远端接口响应中读取 RetInfo 的公共能力。
type retInfoResponse interface {
	GetRetInfo() *pb.RetInfo
}

func exportRowsRemote(ctx context.Context, storage storageInvoker, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	rsp := &pb.ReadTimeSeriesRowsRsp{}
	if err := callStorage(ctx, storage, accessServiceName, "ReadTimeSeriesRows", req, rsp); err != nil {
		return nil, err
	}
	return rsp, nil
}

// callStorage 调用 Storage 并检查业务返回码。
func callStorage(ctx context.Context, storage storageInvoker, service string, method string, req proto.Message, rsp proto.Message) error {
	if err := callStorageRaw(ctx, storage, service, method, req, rsp); err != nil {
		return err
	}
	return checkStorageRetInfo(service, method, rsp)
}

// callStorageRaw 调用 Storage，不检查业务返回码。请求中的 space_id 经元数据 x-space-id 一并传给 Storage。
func callStorageRaw(ctx context.Context, storage storageInvoker, service string, method string, req proto.Message, rsp proto.Message) error {
	if storage == nil {
		return errors.New("存储客户端未配置")
	}
	opts := []gatewayclient.CallOption{gatewayclient.WithTimeout(storageCallTimeout)}
	if spaceID := protoMessageSpaceID(req.ProtoReflect()); spaceID != "" {
		opts = append(opts, gatewayclient.WithMetadata(gatewayroute.MetadataSpaceID, []byte(spaceID)))
	}
	if err := storage.Invoke(ctx, service, method, req, rsp, opts...); err != nil {
		return fmt.Errorf("%s/%s: %w", service, method, err)
	}
	return nil
}

func protoMessageSpaceID(message protoreflect.Message) string {
	if !message.IsValid() {
		return ""
	}
	fields := message.Descriptor().Fields()
	if field := fields.ByName("space_id"); field != nil && field.Kind() == protoreflect.StringKind {
		if value := strings.TrimSpace(message.Get(field).String()); value != "" {
			return value
		}
	}
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		if field.Kind() != protoreflect.MessageKind || field.IsList() || field.IsMap() || !message.Has(field) {
			continue
		}
		if value := protoMessageSpaceID(message.Get(field).Message()); value != "" {
			return value
		}
	}
	return ""
}

func checkStorageRetInfo(service string, method string, rsp proto.Message) error {
	retInfo, ok := responseRetInfo(rsp)
	if !ok {
		return nil
	}
	if retInfo == nil {
		return fmt.Errorf("%s/%s failed: missing ret_info", service, method)
	}
	if retInfo.GetCode() != pb.ErrorCode_SUCCESS {
		return fmt.Errorf("%s/%s failed: %s", service, method, retInfo.GetMsg())
	}
	return nil
}

func responseRetInfo(rsp proto.Message) (*pb.RetInfo, bool) {
	withRet, ok := rsp.(retInfoResponse)
	if !ok {
		return nil, false
	}
	return withRet.GetRetInfo(), true
}
