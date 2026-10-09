package keys

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/security"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const testMaster = "fixture-only-master-key-0123456789abcdef"

func testStoreDB(t *testing.T, path string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"), &gorm.Config{})
	require.NoError(t, err)
	connection, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	return db
}

func testStore(t *testing.T) (*Store, *gorm.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "admin.db")
	db := testStoreDB(t, path)
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)
	require.NoError(t, db.Exec("INSERT INTO t_hosts (c_host_id, c_address) VALUES ('control', '192.0.2.1'), ('storage', '192.0.2.2')").Error)
	store, err := NewStore(db, testMaster)
	require.NoError(t, err)
	return store, db, path
}

func ciphertext(t *testing.T, db *gorm.DB, caller string) string {
	t.Helper()
	var row model.Secret
	require.NoError(t, db.Where("c_secret_id = ?", model.GatewayKeyringIDPrefix+caller).First(&row).Error)
	return row.SecretValue
}

func TestEnsureReusesEncryptedMasterAndNeverFormatsSecret(t *testing.T) {
	store, db, _ := testStore(t)
	ctx := context.Background()
	first, err := store.Ensure(ctx, "console")
	require.NoError(t, err)
	encoded := ciphertext(t, db, "console")
	require.NotContains(t, encoded, first.Credentials().Secret)
	plain, err := security.Decrypt(encoded, testMaster)
	require.NoError(t, err)
	require.Contains(t, plain, first.Credentials().Secret)
	second, err := store.Ensure(ctx, "console")
	require.NoError(t, err)
	require.Equal(t, first.Credentials(), second.Credentials())
	require.Equal(t, encoded, ciphertext(t, db, "console"), "redeployment must not rewrite the master copy")
	for _, format := range []string{"%v", "%+v", "%#v"} {
		require.NotContains(t, fmt.Sprintf(format, first), first.Credentials().Secret)
	}
	metadata, err := json.Marshal(first)
	require.NoError(t, err)
	require.NotContains(t, string(metadata), first.Credentials().Secret)
}

func TestRotationKeepsBothSignaturesUntilExplicitRetirement(t *testing.T) {
	store, _, _ := testStore(t)
	ctx := context.Background()
	old, err := store.Ensure(ctx, "admin")
	require.NoError(t, err)
	current, err := store.Rotate(ctx, "admin")
	require.NoError(t, err)
	require.NotEqual(t, old.KeyID, current.KeyID)
	require.NotEqual(t, old.Credentials().Secret, current.Credentials().Secret)
	again, err := store.Ensure(ctx, "admin")
	require.NoError(t, err)
	require.Equal(t, current.Credentials(), again.Credentials())
	_, err = store.Rotate(ctx, "admin")
	require.ErrorIs(t, err, ErrRotationPending)
	keys, err := store.InternalVerification(ctx, []string{"admin"})
	require.NoError(t, err)
	require.Len(t, keys, 2)
	registry := verificationRegistry(t, keys)
	request := gatewayauth.Request{Method: "POST", Path: "/trpc.moox.ops.SysDeploy/GetCatalog", TargetNode: "control", Callee: "trpc.moox.ops.SysDeploy", Func: "GetCatalog", Body: []byte{8, 1}}
	for _, key := range []SigningKey{old, current} {
		headers, err := gatewayauth.Sign(key.Credentials(), request, time.Now())
		require.NoError(t, err)
		_, err = registry.Verify(request, headers, time.Now())
		require.NoError(t, err)
	}
	require.ErrorIs(t, store.Retire(ctx, "admin", current.KeyID), ErrActiveKey)
	require.ErrorIs(t, store.Retire(ctx, "admin", "missing"), ErrKeyNotFound)
	require.NoError(t, store.Retire(ctx, "admin", old.KeyID))
	keys, err = store.InternalVerification(ctx, []string{"admin"})
	require.NoError(t, err)
	require.Len(t, keys, 1)
	registry = verificationRegistry(t, keys)
	for _, key := range []SigningKey{old, current} {
		headers, err := gatewayauth.Sign(key.Credentials(), request, time.Now())
		require.NoError(t, err)
		_, err = registry.Verify(request, headers, time.Now())
		if key.KeyID == old.KeyID {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
		}
	}
	_, err = store.Rotate(ctx, "admin")
	require.NoError(t, err, "retirement must permit the next controlled rotation")
}

func verificationRegistry(t *testing.T, keys []VerificationKey) *gatewayauth.CredentialRegistry {
	t.Helper()
	var credentials []gatewayauth.Credentials
	for _, key := range keys {
		credentials = append(credentials, gatewayauth.Credentials{Caller: key.Caller, KeyID: key.KeyID, Secret: string(key.Secret)})
	}
	registry, err := gatewayauth.NewCredentialRegistry(credentials)
	require.NoError(t, err)
	return registry
}

func TestEnsureAllAndVerificationIdentityIsolation(t *testing.T) {
	store, db, _ := testStore(t)
	ctx := context.Background()
	first, err := store.EnsureAll(ctx)
	require.NoError(t, err)
	require.Len(t, first, 24) // 17 components + console/CLI + 3 external + 2 hosts.
	second, err := store.EnsureAll(ctx)
	require.NoError(t, err)
	require.Equal(t, first, second)
	internal, err := store.InternalVerification(ctx, []string{"console", "host-gateway@control", "console"})
	require.NoError(t, err)
	require.Len(t, internal, 2)
	require.Equal(t, "console", internal[0].Caller)
	require.Equal(t, "host-gateway@control", internal[1].Caller)
	internal[0].Secret[0] ^= 1
	current, err := store.Current(ctx, "console")
	require.NoError(t, err)
	require.NotEqual(t, current.Credentials().Secret, string(internal[0].Secret))
	external, err := store.ExternalVerification(ctx)
	require.NoError(t, err)
	require.Len(t, external, 3)
	for _, key := range external {
		require.True(t, store.isExternal(key.Caller))
		require.NotContains(t, fmt.Sprintf("%#v", key), string(key.Secret))
	}
	_, err = store.InternalVerification(ctx, []string{"console", "factor-engine"})
	require.ErrorIs(t, err, ErrInvalidCaller)
	for _, caller := range []string{"host-gateway", "host-gateway@absent", "host-gateway@../control", "host-gateway@control ", "unknown", "Console", "console "} {
		_, err := store.Ensure(ctx, caller)
		require.ErrorIs(t, err, ErrInvalidCaller, caller)
	}
	// Disabled registered hosts still retain their signing identity for recovery.
	require.NoError(t, db.Exec("UPDATE t_hosts SET c_status='disabled' WHERE c_host_id='storage'").Error)
	_, err = store.Current(ctx, "host-gateway@storage")
	require.NoError(t, err)
	info, err := store.Info(ctx, "console")
	require.NoError(t, err)
	require.Len(t, info.Keys, 1)
	require.True(t, info.Keys[0].Current)
}

func TestCorruptOrWrongMasterFailsClosedWithoutReplacingKeys(t *testing.T) {
	store, db, _ := testStore(t)
	ctx := context.Background()
	key, err := store.Ensure(ctx, "moox-cli")
	require.NoError(t, err)
	before := ciphertext(t, db, key.Caller)
	wrong, err := NewStore(db, "wrong-master-fixture")
	require.NoError(t, err)
	_, err = wrong.Ensure(ctx, key.Caller)
	require.ErrorIs(t, err, ErrInvalidKeyring)
	require.Equal(t, before, ciphertext(t, db, key.Caller))
	for _, change := range []map[string]any{
		{"c_secret_value": "corrupt"}, {"c_key_id": "wrong"}, {"c_status": "inactive"}, {"c_is_deleted": true},
	} {
		rollback := errors.New("rollback test corruption")
		err := db.Transaction(func(tx *gorm.DB) error {
			require.NoError(t, tx.Model(&model.Secret{}).Where("c_secret_id = ?", model.GatewayKeyringIDPrefix+key.Caller).Updates(change).Error)
			temporary, err := NewStore(tx, testMaster)
			require.NoError(t, err)
			_, err = temporary.ensure(tx, key.Caller)
			require.ErrorIs(t, err, ErrInvalidKeyring)
			return rollback
		})
		require.ErrorIs(t, err, rollback)
	}
}

func TestEnsureAllRollsBackPartialProvisioning(t *testing.T) {
	store, db, _ := testStore(t)
	_, err := store.Ensure(context.Background(), "admin")
	require.NoError(t, err)
	before := ciphertext(t, db, "admin")
	require.NoError(t, db.Exec("CREATE TRIGGER reject_cli_key BEFORE INSERT ON t_secrets WHEN NEW.c_secret_id = 'moox:gateway-keyring:moox-cli' BEGIN SELECT RAISE(ABORT, 'fixture rejection'); END").Error)
	items, err := store.EnsureAll(context.Background())
	require.Error(t, err)
	require.Nil(t, items)
	var count int64
	require.NoError(t, db.Model(&model.Secret{}).Count(&count).Error)
	require.Equal(t, int64(1), count)
	require.Equal(t, before, ciphertext(t, db, "admin"))
}

func TestIndependentStoresSerializeEnsureAndRotation(t *testing.T) {
	first, _, path := testStore(t)
	second, err := NewStore(testStoreDB(t, path), testMaster)
	require.NoError(t, err)
	stores := []*Store{first, second}
	var wg sync.WaitGroup
	keys := make([]SigningKey, 2)
	errorsSeen := make([]error, 2)
	start := make(chan struct{})
	for i, store := range stores {
		wg.Add(1)
		go func(i int, store *Store) {
			defer wg.Done()
			<-start
			keys[i], errorsSeen[i] = store.Ensure(context.Background(), "console")
		}(i, store)
	}
	close(start)
	wg.Wait()
	for _, err := range errorsSeen {
		require.NoError(t, err)
	}
	require.Equal(t, keys[0].Credentials(), keys[1].Credentials())
	oldID := keys[0].KeyID
	start = make(chan struct{})
	for i, store := range stores {
		wg.Add(1)
		go func(i int, store *Store) {
			defer wg.Done()
			<-start
			keys[i], errorsSeen[i] = store.Rotate(context.Background(), "console")
		}(i, store)
	}
	close(start)
	wg.Wait()
	require.Equal(t, 1, len(slices.DeleteFunc(slices.Clone(errorsSeen), func(err error) bool { return err != nil })))
	for _, err := range errorsSeen {
		if err != nil {
			require.ErrorIs(t, err, ErrRotationPending)
		}
	}
	info, err := first.Info(context.Background(), "console")
	require.NoError(t, err)
	require.Len(t, info.Keys, 2)
	require.True(t, slices.ContainsFunc(info.Keys, func(k KeyInfo) bool { return k.KeyID == oldID && !k.Current }))
}

func TestKeyringValidationRejectsUntrustedPlaintext(t *testing.T) {
	item, err := newEntry()
	require.NoError(t, err)
	valid := keyring{Version: 1, Caller: "console", Current: item.KeyID, Entries: []entry{item}}
	require.True(t, validRing(valid, "console"))
	for _, mutate := range []func(*keyring){
		func(r *keyring) { r.Version = 2 }, func(r *keyring) { r.Caller = "admin" },
		func(r *keyring) { r.Current = "absent" }, func(r *keyring) { r.Entries = append(r.Entries, r.Entries[0]) },
		func(r *keyring) { r.Entries[0].Secret = "short" }, func(r *keyring) { r.Entries[0].CreatedAt = 0 },
		func(r *keyring) { r.Entries[0].KeyID = strings.Repeat("A", 32); r.Current = r.Entries[0].KeyID },
	} {
		copy := valid
		copy.Entries = slices.Clone(valid.Entries)
		mutate(&copy)
		require.False(t, validRing(copy, "console"))
	}
}
