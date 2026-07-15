package command

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const remoteWriteBatchSize = 1000

// retInfoResponse 定义远端接口响应中读取 RetInfo 的公共能力。
type retInfoResponse interface {
	GetRetInfo() *pb.RetInfo
}

func exportRowsRemote(ctx context.Context, storageURL string, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	rsp := &pb.ReadTimeSeriesRowsRsp{}
	if err := postStorage(ctx, storageURL, accessServiceName, "ReadTimeSeriesRows", req, rsp); err != nil {
		return nil, err
	}
	return rsp, nil
}

func postStorage(ctx context.Context, storageURL string, service string, method string, req proto.Message, rsp proto.Message) error {
	if err := postStorageRaw(ctx, storageURL, service, method, req, rsp); err != nil {
		return err
	}
	return checkStorageRetInfo(service, method, rsp)
}

func postStorageRaw(ctx context.Context, storageURL string, service string, method string, req proto.Message, rsp proto.Message) error {
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(req)
	if err != nil {
		return err
	}
	url := strings.TrimRight(storageURL, "/") + "/" + service + "/" + method
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if spaceID := protoMessageSpaceID(req.ProtoReflect()); spaceID != "" {
		httpReq.Header.Set("X-Space-Id", spaceID)
	}
	client := &http.Client{Timeout: 60 * time.Second}
	httpRsp, err := client.Do(httpReq)
	if err != nil {
		return err
	}
	defer httpRsp.Body.Close()
	body, _ := io.ReadAll(httpRsp.Body)
	if httpRsp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s/%s HTTP %d: %s", service, method, httpRsp.StatusCode, string(body))
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(body, rsp); err != nil {
		return err
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
