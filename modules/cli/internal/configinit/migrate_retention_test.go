package configinit

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/configcrypt"
)

func TestMigrateSetsRetentionKeepingOtherFrequencies(t *testing.T) {
	text := strings.Replace(sampleText(t), `"1m" = "35d"`, `"1m" = "8d"`, 1)
	require.Contains(t, text, `"1m" = "8d"`)
	path, root, keys := migrateFixture(t, text)

	at := time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC)
	report, err := MigrateFile(path, MigrateOptions{RepoRoot: root, Keys: keys, RetentionTarget: "defaults.1m", RetentionValue: "35d", Write: true, Now: func() time.Time { return at }})
	require.NoError(t, err)
	require.True(t, report.Changed && report.Written)
	require.Equal(t, map[string]string{"defaults.1m": "35d"}, report.Retention)

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	plain, err := configcrypt.Decrypt(raw, keys)
	require.NoError(t, err)
	manifest, err := setupconfig.Parse(plain)
	require.NoError(t, err)
	require.Equal(t, "35d", manifest.StorageRetention.Defaults["1m"])
	require.Equal(t, "35d", manifest.StorageRetention.Defaults["5m"], "其他频率不变")
	require.Equal(t, "60d", manifest.StorageRetention.Spaces["stockcn"]["1m"], "空间覆盖不变")

	later := at.Add(time.Minute)
	_, err = MigrateFile(path, MigrateOptions{RepoRoot: root, Keys: keys, RetentionTarget: "stockcn.1m", RetentionValue: "90d", Write: true, Now: func() time.Time { return later }})
	require.NoError(t, err)
	raw, _ = os.ReadFile(path)
	plain, _ = configcrypt.Decrypt(raw, keys)
	manifest, err = setupconfig.Parse(plain)
	require.NoError(t, err)
	require.Equal(t, "90d", manifest.StorageRetention.Spaces["stockcn"]["1m"])
}

func TestSetRetentionRejectsMissingTableAndBadTarget(t *testing.T) {
	_, err := setStorageRetention([]byte("[factors]\nenabled = true\n"), "defaults.1m", "35d")
	require.ErrorContains(t, err, "内置默认值")
	_, err = setStorageRetention([]byte("[storage_retention.defaults]\n\"1m\" = \"8d\"\n"), "1m", "35d")
	require.ErrorContains(t, err, "defaults.<频率>")
}
