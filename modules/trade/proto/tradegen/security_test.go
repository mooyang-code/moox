package tradepb

import (
	"context"
	"testing"

	"trpc.group/trpc-go/trpc-filter/masking"
)

func TestMaskingFilterRedactsTradeCredentials(t *testing.T) {
	filter := masking.ServerFilter()
	rsp, err := filter(context.Background(), nil, func(context.Context, interface{}) (interface{}, error) {
		return &ListApiKeysRsp{ApiKeys: []*ApiKey{{ApiKey: "plain-api-key", Passphrase: "plain-passphrase"}}}, nil
	})
	if err != nil {
		t.Fatalf("filter error = %v", err)
	}
	got := rsp.(*ListApiKeysRsp).GetApiKeys()[0]
	if got.GetApiKey() == "plain-api-key" || got.GetApiKey() == "" {
		t.Fatalf("api key was not masked: %q", got.GetApiKey())
	}
	if got.GetPassphrase() == "plain-passphrase" || got.GetPassphrase() == "" {
		t.Fatalf("passphrase was not masked: %q", got.GetPassphrase())
	}
}
