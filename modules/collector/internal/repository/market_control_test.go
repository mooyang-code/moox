package repository

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestMarketControlAcquirePermitIsAtomicAndIdempotent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "control.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateMarketControl(db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 11, 0, 0, 30, 0, time.UTC)
	repo := NewMarketControlRepository(db)
	if err := repo.PutLease(context.Background(), MarketLease{LeaseID: "lease-1", LeaseType: "provider", LeaseKey: "binance", Epoch: 7, OwnerID: "job-1", ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	req := PermitRequest{ProviderID: "binance", ScopeKey: "ip:1", EndpointClass: "klines", Cost: 2, LeaseID: "lease-1", LeaseEpoch: 7, ExecutionNonce: "nonce", RequestIndex: 1, Now: now, Windows: []QuotaWindow{{WindowSeconds: 60, Limit: 3}}}
	first, err := repo.AcquirePermit(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Allowed {
		t.Fatalf("first denied: %+v", first)
	}
	replay, err := repo.AcquirePermit(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if replay.PermitID != first.PermitID {
		t.Fatalf("replay changed permit: %+v %+v", first, replay)
	}
	req.ExecutionNonce = "nonce-2"
	req.RequestIndex = 2
	denied, err := repo.AcquirePermit(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if denied.Allowed || denied.DenialReason != "quota_exhausted" {
		t.Fatalf("over limit permit=%+v", denied)
	}
}

func TestMarketControlRejectsExpiredOrWrongEpochLease(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "control.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateMarketControl(db); err != nil {
		t.Fatal(err)
	}
	repo := NewMarketControlRepository(db)
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	_ = repo.PutLease(context.Background(), MarketLease{LeaseID: "old", LeaseType: "resolution", LeaseKey: "key", Epoch: 2, OwnerID: "job", ExpiresAt: now.Add(-time.Second)})
	if err := repo.ValidateLease(context.Background(), "old", "resolution", 2, now); err == nil {
		t.Fatal("expired lease accepted")
	}
}

func TestTryAcquireLeaseGroupDoesNotStealActiveLeaseOrPartiallyWrite(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "leases.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := MigrateMarketControl(db); err != nil {
		t.Fatal(err)
	}
	repo := NewMarketControlRepository(db)
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	active := MarketLease{LeaseID: "resolution", LeaseType: "resolution", LeaseKey: "scope", Epoch: 1, OwnerID: "active", ExpiresAt: now.Add(time.Minute)}
	if err := repo.TryAcquireLeaseGroup(context.Background(), []MarketLease{active}, now); err != nil {
		t.Fatal(err)
	}
	err = repo.TryAcquireLeaseGroup(context.Background(), []MarketLease{{LeaseID: "provider", LeaseType: "provider", LeaseKey: "provider", Epoch: 2, OwnerID: "fallback", ExpiresAt: now.Add(time.Minute)}, {LeaseID: "resolution", LeaseType: "resolution", LeaseKey: "scope", Epoch: 2, OwnerID: "fallback", ExpiresAt: now.Add(time.Minute)}}, now)
	if _, ok := err.(*LeaseBusyError); !ok {
		t.Fatalf("error=%T %v", err, err)
	}
	var count int64
	if err := db.Model(&MarketLease{}).Where("c_lease_id = ?", "provider").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("partial provider lease count=%d err=%v", count, err)
	}
	var stored MarketLease
	if err := db.Where("c_lease_id = ?", "resolution").Take(&stored).Error; err != nil || stored.OwnerID != "active" || stored.Epoch != 1 {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
}
