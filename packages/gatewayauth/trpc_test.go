package gatewayauth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/structpb"
	"trpc.group/trpc-go/trpc-go/codec"
)

func TestTRPCClientFilterSignsExactMapContainingWireBody(t *testing.T) {
	credentials := Credentials{KeyID: "collector", Secret: "secret"}
	targetNode := "control"
	signedAt := time.Unix(1_700_000_000, 0)
	clientFilter := NewTRPCClientFilter(credentials, targetNode, func() time.Time { return signedAt })

	ctx, msg := codec.EnsureMessage(context.Background())
	msg.WithClientRPCName("/trpc.moox.storage.Metadata/RegisterDataSubject")
	msg.WithCalleeServiceName("trpc.moox.storage.Metadata")
	msg.WithCalleeMethod("RegisterDataSubject")
	msg.WithSerializationType(codec.SerializationTypePB)

	req, err := structpb.NewStruct(map[string]any{
		"attributes": map[string]any{"quote_asset": "USDT", "base_asset": "BTC"},
	})
	if err != nil {
		t.Fatal(err)
	}
	rsp := &structpb.Struct{}
	err = clientFilter(ctx, req, rsp, func(ctx context.Context, rawReq, rawRsp interface{}) error {
		requestBody, ok := rawReq.(*codec.Body)
		if !ok {
			t.Fatalf("request type = %T, want *codec.Body", rawReq)
		}
		headers := make(http.Header)
		for key, value := range codec.Message(ctx).ClientMetaData() {
			headers.Set(key, string(value))
		}
		if _, err := Verify(credentials, Request{
			Method: http.MethodPost, Path: "/trpc.moox.storage.Metadata/RegisterDataSubject",
			TargetNode: targetNode, Callee: "trpc.moox.storage.Metadata",
			Func: "RegisterDataSubject", Body: requestBody.Data,
		}, headers, signedAt); err != nil {
			t.Fatalf("verify exact wire body: %v", err)
		}
		response, err := structpb.NewStruct(map[string]any{"status": "ok"})
		if err != nil {
			return err
		}
		data, err := codec.Marshal(codec.SerializationTypePB, response)
		if err != nil {
			return err
		}
		rawRsp.(*codec.Body).Data = data
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := rsp.GetFields()["status"].GetStringValue(); got != "ok" {
		t.Fatalf("response status = %q, want ok", got)
	}
}
