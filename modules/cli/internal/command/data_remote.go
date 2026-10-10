package command

import (
	"context"
	"fmt"
	"strings"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// retInfoResponse 定义远端接口响应中读取 RetInfo 的公共能力。
type retInfoResponse interface {
	GetRetInfo() *pb.RetInfo
}

func exportRowsRemote(ctx context.Context, gateway gatewayclient.Invoker, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	rsp := &pb.ReadTimeSeriesRowsRsp{}
	if err := postStorage(ctx, gateway, accessServiceName, "ReadTimeSeriesRows", req, rsp); err != nil {
		return nil, err
	}
	return rsp, nil
}

func postStorage(ctx context.Context, gateway gatewayclient.Invoker, service string, method string, req proto.Message, rsp proto.Message) error {
	if err := postStorageRaw(ctx, gateway, service, method, req, rsp); err != nil {
		return err
	}
	return checkStorageRetInfo(service, method, rsp)
}

func postStorageRaw(ctx context.Context, gateway gatewayclient.Invoker, service string, method string, req proto.Message, rsp proto.Message) error {
	if gateway == nil || req == nil || rsp == nil {
		return fmt.Errorf("Storage call requires the operator SSH gateway and request/response")
	}
	metadata := gatewayclient.CallMetadataFromContext(ctx)
	if spaceID := protoMessageSpaceID(req.ProtoReflect()); spaceID != "" {
		metadata.SpaceID = spaceID
	}
	return gateway.Invoke(gatewayclient.WithCallMetadata(ctx, metadata), service, method, req, rsp)
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
