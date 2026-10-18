package unitwatch

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/hostpolicy"
	"github.com/stretchr/testify/require"
)

// fakeCrontab 模拟部署用户的 crontab。
type fakeCrontab struct{ content string }

func (f *fakeCrontab) run(args []string, stdin []byte) ([]byte, error) {
	switch args[0] {
	case "-l":
		return []byte(f.content), nil
	case "-":
		f.content = string(stdin)
		return nil, nil
	}
	return nil, errors.New("unexpected crontab call")
}

func installFixture(t *testing.T, existing string) (InstallOptions, *fakeCrontab) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "run"), 0o700))
	fake := &fakeCrontab{content: existing}
	return InstallOptions{Root: root, crontab: fake.run, cronActive: func() error { return nil }}, fake
}

// 安装：保留用户自己的条目，删掉旧的 moox-healthchecks，只留一条 watch；重复安装不产生重复条目。
func TestInstallIsIdempotentAndRemovesLegacyEntry(t *testing.T) {
	opts, fake := installFixture(t, "0 3 * * * /usr/bin/backup\n* * * * * for h in /data/moox/*/healthcheck.sh; do $h; done # moox-healthchecks\n")
	result, err := Install(opts)
	require.NoError(t, err)
	require.Equal(t, 1, result.LegacyRemoved)
	require.Equal(t, CronLine(opts.Root, filepath.Join(opts.Root, "host", "current", "bin", "moox-runtime")), result.CronLine)
	require.Contains(t, fake.content, "/usr/bin/backup")
	require.NotContains(t, fake.content, "moox-healthchecks")
	require.Equal(t, 1, strings.Count(fake.content, CronMarker))
	require.Contains(t, result.CronLine, "flock -n "+filepath.Join(opts.Root, "run", "watchdog.lock"))
	require.Contains(t, result.CronLine, "watch --root "+opts.Root+" --log "+filepath.Join(opts.Root, "run", "watchdog.log"))

	again, err := Install(opts)
	require.NoError(t, err)
	require.Zero(t, again.LegacyRemoved)
	require.Equal(t, 1, strings.Count(fake.content, CronMarker))
}

// 卸载只删 watch 条目；cron 没在运行时安装报错而不是静默跳过。
func TestInstallRemoveAndCronCheck(t *testing.T) {
	opts, fake := installFixture(t, "0 3 * * * /usr/bin/backup\n")
	_, err := Install(opts)
	require.NoError(t, err)
	opts.Remove = true
	removed, err := Install(opts)
	require.NoError(t, err)
	require.True(t, removed.Removed)
	require.Equal(t, "0 3 * * * /usr/bin/backup\n", fake.content)

	opts.Remove = false
	opts.cronActive = func() error { return errors.New("cron daemon is not running") }
	_, err = Install(opts)
	require.ErrorContains(t, err, "cron daemon is not running")
	require.Equal(t, "0 3 * * * /usr/bin/backup\n", fake.content, "检查失败时不改 crontab")
}

// 种子：没有缓存时写入；内容相同不重写；已有从 Monitor 拉取的策略时不覆盖。
func TestInstallSeedNeverOverridesMonitorPolicy(t *testing.T) {
	opts, _ := installFixture(t, "")
	seed := hostpolicy.Policy{FetchedAt: time.Now(), NotificationChannels: []hostpolicy.Channel{{Type: "wecom", WebhookURL: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=seed"}}}
	opts.Seed = &seed
	result, err := Install(opts)
	require.NoError(t, err)
	require.Equal(t, "written", result.Seed)
	saved, err := hostpolicy.Read(hostpolicy.Path(opts.Root))
	require.NoError(t, err)
	require.Equal(t, hostpolicy.SourceSeed, saved.Source)

	result, err = Install(opts)
	require.NoError(t, err)
	require.Equal(t, "unchanged", result.Seed)

	saved.Source = hostpolicy.SourceMonitor
	saved.NotificationChannels = []hostpolicy.Channel{{Type: "feishu", WebhookURL: "https://open.feishu.cn/open-apis/bot/v2/hook/x"}}
	require.NoError(t, hostpolicy.Write(hostpolicy.Path(opts.Root), saved))
	result, err = Install(opts)
	require.NoError(t, err)
	require.Equal(t, "kept_monitor", result.Seed)
	kept, err := hostpolicy.Read(hostpolicy.Path(opts.Root))
	require.NoError(t, err)
	require.Equal(t, "feishu", kept.NotificationChannels[0].Type)
}

func TestOpenLogRotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watchdog.log")
	require.NoError(t, os.WriteFile(path, make([]byte, logMaxBytes), 0o600))
	file, err := OpenLog(path)
	require.NoError(t, err)
	_, err = file.WriteString("{}\n")
	require.NoError(t, err)
	require.NoError(t, file.Close())
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, int64(3), info.Size())
	require.FileExists(t, path+".1")
}
