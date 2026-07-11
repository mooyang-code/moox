package rpc

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/repository"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"google.golang.org/protobuf/types/known/structpb"
	"gorm.io/gorm"
)

func TestFinalizeAndGetMarketAttemptReceiptRPCIsIdempotent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:attempt-rpc?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&domain.TaskInstance{}); err != nil {
		t.Fatal(err)
	}
	if err := repository.MigrateMarketControl(db); err != nil {
		t.Fatal(err)
	}
	service := New(db, Dependencies{})
	summary, _ := structpb.NewStruct(map[string]any{"rows": float64(1)})
	payload, _ := structpb.NewStruct(map[string]any{"cursor": "next"})
	req := &pb.FinalizeMarketAttemptReq{JobItemId: "job", AttemptNo: 1, MarketId: "crypto_binance", SpaceId: "crypto_binance", ProviderId: "binance", Feed: "kline", Phase: "fetch", Status: "success", Summary: summary, Outbox: []*pb.MarketAttemptOutbox{{Kind: "continuation", Payload: payload}}}
	first, err := service.FinalizeMarketAttempt(context.Background(), req)
	if err != nil || first.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || first.GetAlreadyFinalized() {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := service.FinalizeMarketAttempt(context.Background(), req)
	if err != nil || !second.GetAlreadyFinalized() {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	got, err := service.GetMarketAttemptReceipt(context.Background(), &pb.GetMarketAttemptReceiptReq{JobItemId: "job", AttemptNo: 1})
	if err != nil || !got.GetFound() || got.GetReceipt().GetSummary().AsMap()["rows"] != float64(1) {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}
