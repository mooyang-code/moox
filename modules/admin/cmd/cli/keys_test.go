package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/admin/internal/service/keys"
	"github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayauth/authtest"
	"github.com/stretchr/testify/require"
)

type keysCLIFixture struct{ db string }

func newKeysCLI(t *testing.T) keysCLIFixture {
	t.Helper()
	f := keysCLIFixture{db: filepath.Join(t.TempDir(), "admin.db")}
	require.NoError(t, applySchema(f.db, schema.AdminSQL()))
	require.NoError(t, os.Chmod(f.db, 0o600))
	db, err := openAdminCLIDB(f.db)
	require.NoError(t, err)
	require.NoError(t, db.Exec("INSERT INTO t_hosts (c_host_id, c_address) VALUES ('control', '192.0.2.1')").Error)
	closeAdminCLIDB(db)
	return f
}

func (f keysCLIFixture) run(sub string, flags ...string) (string, error) {
	args := append([]string{"keys", sub, "--db-path", f.db}, flags...)
	var out, stderr bytes.Buffer
	err := runKeysCommand(args, &out, &stderr)
	return out.String(), err
}

func identityPublicKey(label string) string {
	return gatewayauth.EncodePublicKey(authtest.Identity(label).PublicKey())
}

func TestKeysCLISetListDelete(t *testing.T) {
	f := newKeysCLI(t)
	first, second := identityPublicKey("first"), identityPublicKey("second")
	_, err := f.run("set", "--identity", "moox-cli", "--public-key", first)
	require.NoError(t, err)
	// 再登记一次就是替换：一个身份只有一把公钥。
	_, err = f.run("set", "--identity", "moox-cli", "--public-key", second)
	require.NoError(t, err)
	_, err = f.run("set", "--identity", "scf-collector", "--public-key", first)
	require.NoError(t, err)
	out, err := f.run("list")
	require.NoError(t, err)
	var listing struct {
		Hosts      []keys.HostKey     `json:"hosts"`
		Identities []keys.IdentityKey `json:"identities"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &listing))
	require.Equal(t, []keys.HostKey{{HostID: "control"}}, listing.Hosts)
	require.Len(t, listing.Identities, 2)
	require.Equal(t, "moox-cli", listing.Identities[0].Identity)
	require.Equal(t, second, listing.Identities[0].PublicKey)
	_, err = f.run("delete", "--identity", "scf-collector")
	require.NoError(t, err)
	_, err = f.run("delete", "--identity", "scf-collector")
	require.ErrorIs(t, err, keys.ErrNotRegistered)
}

func TestKeysCLIRejectsComponentIdentitiesAndRemovedSubcommands(t *testing.T) {
	f := newKeysCLI(t)
	key := identityPublicKey("x")
	for _, identity := range []string{"console", "host-gateway", "trade", "factor-engine", "moox-skill", "unknown"} {
		_, err := f.run("set", "--identity", identity, "--public-key", key)
		require.ErrorIs(t, err, keys.ErrInvalidIdentity, identity)
	}
	_, err := f.run("set", "--identity", "moox-cli", "--public-key", "not-a-key")
	require.ErrorIs(t, err, keys.ErrInvalidPublicKey)
	for _, args := range [][]string{
		{"set", "--identity", "moox-cli"},
		{"set", "--public-key", key},
		{"delete"},
		{"delete", "--identity", "moox-cli", "--public-key", key},
		{"list", "--identity", "moox-cli"},
		{"list", "extra"},
	} {
		_, err := f.run(args[0], args[1:]...)
		require.Error(t, err, args)
	}
	// 私钥不在 Admin 里：导出、轮换并存、退役这些命令都没有了。
	for _, removed := range []string{"ensure", "export", "export-access", "rotate", "retire", "info"} {
		_, err := f.run(removed)
		require.ErrorContains(t, err, "unknown keys subcommand", removed)
	}
	missing := keysCLIFixture{db: filepath.Join(t.TempDir(), "missing.db")}
	_, err = missing.run("list")
	require.Error(t, err)
}
