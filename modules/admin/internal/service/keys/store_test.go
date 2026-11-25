package keys

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayauth/authtest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func testStore(t *testing.T) (*Store, *gorm.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "admin.db")
	db, err := gorm.Open(sqlite.Open("file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"), &gorm.Config{})
	require.NoError(t, err)
	connection, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	require.NoError(t, db.Exec(schema.AdminSQL()).Error)
	require.NoError(t, db.Exec("INSERT INTO t_hosts (c_host_id, c_address) VALUES ('control', '192.0.2.1'), ('storage', '192.0.2.2')").Error)
	store, err := NewStore(db)
	require.NoError(t, err)
	return store, db
}

func public(label string) string {
	return gatewayauth.EncodePublicKey(authtest.Identity(label).PublicKey())
}

// 只有 moox-cli 与目录里的外部身份能登记公钥：组件名、console、主机网关登记成身份会让持有私钥的人不经主机冒充它们。
func TestOnlyNonHostIdentitiesCanBeRegistered(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	for _, identity := range []string{"moox-cli", "scf-collector", "moox-skill"} {
		require.NoError(t, store.SetIdentity(ctx, identity, public(identity)), identity)
	}
	for _, identity := range []string{"console", "host-gateway", "host-gateway@control", "trade", "factor-engine", "archive-worker", "access", "admin", "", "unknown", "MOOX-CLI"} {
		require.ErrorIs(t, store.SetIdentity(ctx, identity, public("x")), ErrInvalidIdentity, identity)
		_, err := store.EnsureIdentity(ctx, identity, public("x"))
		require.ErrorIs(t, err, ErrInvalidIdentity, identity)
	}
	for _, key := range []string{"", "not-base64", "c2hvcnQ=", public("x") + " "} {
		require.ErrorIs(t, store.SetIdentity(ctx, "moox-cli", key), ErrInvalidPublicKey, key)
	}
	rows, err := store.Identities(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	require.Equal(t, []string{"moox-cli", "moox-skill", "scf-collector"}, []string{rows[0].Identity, rows[1].Identity, rows[2].Identity}, "按名字排序")
}

// 一个身份只有一把公钥：SetIdentity 直接替换（换电脑），EnsureIdentity 只建不改（bootstrap 重复执行）。
func TestSetReplacesAndEnsureKeepsTheRegisteredKey(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	first, second := public("first"), public("second")
	registered, err := store.EnsureIdentity(ctx, "moox-cli", first)
	require.NoError(t, err)
	require.Equal(t, first, registered)
	registered, err = store.EnsureIdentity(ctx, "moox-cli", second)
	require.NoError(t, err)
	require.Equal(t, first, registered, "已登记的公钥不被 bootstrap 改掉")
	require.NoError(t, store.SetIdentity(ctx, "moox-cli", second))
	registered, err = store.Identity(ctx, "moox-cli")
	require.NoError(t, err)
	require.Equal(t, second, registered)
	rows, err := store.Identities(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1, "没有新旧并存")
	require.NoError(t, store.DeleteIdentity(ctx, "moox-cli"))
	_, err = store.Identity(ctx, "moox-cli")
	require.ErrorIs(t, err, ErrNotRegistered)
	require.ErrorIs(t, store.DeleteIdentity(ctx, "moox-cli"), ErrNotRegistered)
}

func TestHostPublicKeys(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	key, err := store.HostPublicKey(ctx, "storage")
	require.NoError(t, err)
	require.Empty(t, key, "登记了但还没签发过身份包")
	require.NoError(t, store.SetHostPublicKey(ctx, "storage", public("storage")))
	key, err = store.HostPublicKey(ctx, "storage")
	require.NoError(t, err)
	require.Equal(t, public("storage"), key)
	require.ErrorIs(t, store.SetHostPublicKey(ctx, "absent", public("x")), gorm.ErrRecordNotFound)
	require.ErrorIs(t, store.SetHostPublicKey(ctx, "storage", "bad"), ErrInvalidPublicKey)
	_, err = store.HostPublicKey(ctx, "absent")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	hosts, err := store.Hosts(ctx)
	require.NoError(t, err)
	require.Equal(t, []HostKey{{HostID: "control"}, {HostID: "storage", PublicKey: public("storage")}}, hosts)
}

// 手工改库写进去的非法身份或坏公钥不会进快照。
func TestIdentitiesSkipsRowsThatCouldImpersonateComponents(t *testing.T) {
	store, db := testStore(t)
	ctx := context.Background()
	require.NoError(t, store.SetIdentity(ctx, "moox-skill", public("skill")))
	require.NoError(t, db.Exec("INSERT INTO t_identity_keys (c_identity, c_sign_public_key) VALUES ('trade', ?), ('moox-cli', 'corrupt')", public("rogue")).Error)
	rows, err := store.Identities(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "moox-skill", rows[0].Identity)
}
