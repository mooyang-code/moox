package rpc

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/collector/internal/repository"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"gorm.io/gorm"
)

func TestAcquireProviderPermitUsesDurableQuotaAndLease(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.MigrateMarketControl(db); err != nil {
		t.Fatal(err)
	}
	repo := repository.NewMarketControlRepository(db)
	if err := repo.PutLease(context.Background(), repository.MarketLease{LeaseID: "lease", LeaseType: "provider", LeaseKey: "binance", Epoch: 3, OwnerID: "job", ExpiresAt: time.Now().UTC().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	svc := New(db, Dependencies{})
	rsp, err := svc.AcquireProviderPermit(context.Background(), &pb.AcquireProviderPermitReq{ProviderId: "binance", ScopeKey: "ip", EndpointClass: "klines", RequestCost: 1, QuotaLeaseId: "lease", LeaseEpoch: 3, ExecutionNonce: "nonce", RequestIndex: 1, Windows: []*pb.ProviderQuotaWindow{{WindowSeconds: 60, Limit: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || !rsp.GetAllowed() {
		t.Fatalf("rsp=%+v", rsp)
	}
}
