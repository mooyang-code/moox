package command

import (
	"context"
	"fmt"
	"os"
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

// exportRowsPaged 按游标顺序读完时间区间，最多导出 maxRows 行；达到上限时停止并在标准错误提示还有更多数据。
func exportRowsPaged(ctx context.Context, gateway gatewayclient.Invoker, req *pb.ReadTimeSeriesRowsReq, maxRows uint32) (*pb.ReadTimeSeriesRowsRsp, error) {
	if maxRows == 0 {
		maxRows = 1000
	}
	out := &pb.ReadTimeSeriesRowsRsp{}
	cursor := ""
	for {
		page := proto.Clone(req).(*pb.ReadTimeSeriesRowsReq)
		page.Page = &pb.Page{Size: min(maxRows-uint32(len(out.Rows)), 2000), Cursor: cursor}
		rsp := &pb.ReadTimeSeriesRowsRsp{}
		if err := postStorage(ctx, gateway, accessServiceName, "ReadTimeSeriesRows", page, rsp); err != nil {
			return nil, err
		}
		out.Rows = append(out.Rows, rsp.GetRows()...)
		cursor = rsp.GetPageResult().GetNextCursor()
		if cursor == "" {
			return out, nil
		}
		if uint32(len(out.Rows)) >= maxRows {
			fmt.Fprintf(os.Stderr, "已导出 %d 行，达到 --page-size 上限，区间内还有更多数据\n", len(out.Rows))
			out.PageResult = &pb.PageResult{HasMore: true, NextCursor: cursor}
			return out, nil
		}
	}
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
