package dao

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/control/internal/service/cloudnode/model"
	"gorm.io/gorm"
)

func TestUpdateCloudAccountUpdatesSecretIDPreservesSecretKeyAndCloudFields(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.CloudAccount{}); err != nil {
		t.Fatal(err)
	}
	store := NewCloudAccountDAO(db)
	ctx := t.Context()

	if err := store.CreateCloudAccount(ctx, &model.CloudAccount{
		AccountID:   "acct-1",
		AccountName: "old",
		Provider:    model.CloudProviderTencent,
		SecretID:    "old-secret-id",
		SecretKey:   "old-secret-key",
		AppID:       "old-app",
		COSRegion:   "old-region",
		COSBucket:   "old-bucket",
	}); err != nil {
		t.Fatal(err)
	}

	if err := store.UpdateCloudAccount(ctx, &model.CloudAccount{
		AccountID:   "acct-1",
		AccountName: "new",
		Provider:    model.CloudProviderTencent,
		SecretID:    "new-secret-id",
		SecretKey:   "",
		AppID:       "new-app",
		COSRegion:   "new-region",
		COSBucket:   "new-bucket",
		ExtraConfig: `{"region":"ap-guangzhou"}`,
	}); err != nil {
		t.Fatal(err)
	}

	account, err := store.GetCloudAccount(ctx, "acct-1")
	if err != nil {
		t.Fatal(err)
	}
	if account.SecretID != "new-secret-id" {
		t.Fatalf("SecretID = %q, want new-secret-id", account.SecretID)
	}
	if account.SecretKey != "old-secret-key" {
		t.Fatalf("SecretKey = %q, want old-secret-key", account.SecretKey)
	}
	if account.AppID != "new-app" || account.COSRegion != "new-region" || account.COSBucket != "new-bucket" {
		t.Fatalf("cloud fields = app:%q region:%q bucket:%q", account.AppID, account.COSRegion, account.COSBucket)
	}
}
