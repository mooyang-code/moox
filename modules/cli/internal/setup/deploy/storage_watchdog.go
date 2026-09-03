package deploy

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
)

const (
	storageViewWatchdogScriptName = "moox-storage-view-watchdog"
	storageViewWatchdogService    = "moox-storage-view-watchdog.service"
	storageViewWatchdogTimer      = "moox-storage-view-watchdog.timer"
)

// InstallStorageViewWatchdog installs and enables the host-level recovery loop
// for the independently deployed Storage runtime. The files live in the
// repository so the CLI can install the same checked-in version during setup.
func InstallStorageViewWatchdog(ctx context.Context, transport setupssh.Client, repositoryRoot string, storageRoots ...string) error {
	options := WatchdogOptions{}
	if len(storageRoots) > 0 {
		options.StorageRoot = storageRoots[0]
	}
	return InstallStorageViewWatchdogWithOptions(ctx, transport, repositoryRoot, options)
}

type WatchdogOptions struct {
	StorageRoot   string
	EventBusURL   string
	GatewayNodeID string
}

func InstallStorageViewWatchdogWithOptions(ctx context.Context, transport setupssh.Client, repositoryRoot string, options WatchdogOptions) error {
	if transport == nil || strings.TrimSpace(repositoryRoot) == "" {
		return fmt.Errorf("storage_watchdog_install_invalid")
	}

	home, user, group, err := remoteIdentity(ctx, transport)
	if err != nil {
		return fmt.Errorf("storage_watchdog_remote_identity_failed")
	}
	serviceRoot := setupconfig.DefaultStorageRoot
	if strings.TrimSpace(options.StorageRoot) != "" {
		serviceRoot = filepath.Clean(strings.TrimSpace(options.StorageRoot))
	}
	serviceRoot = filepath.ToSlash(serviceRoot)
	eventBusURL := strings.TrimSpace(options.EventBusURL)
	if eventBusURL == "" {
		eventBusURL = "tls://127.0.0.1:4222"
	}
	gatewayNodeID := strings.TrimSpace(options.GatewayNodeID)
	if gatewayNodeID == "" {
		gatewayNodeID = "control"
	}

	script, err := readWatchdogAsset(repositoryRoot, filepath.Join("scripts", storageViewWatchdogScriptName+".sh"))
	if err != nil {
		return fmt.Errorf("storage_watchdog_asset_invalid")
	}
	service, err := readWatchdogAsset(repositoryRoot, filepath.Join("deploy", "systemd", "system", storageViewWatchdogService))
	if err != nil {
		return fmt.Errorf("storage_watchdog_asset_invalid")
	}
	timer, err := readWatchdogAsset(repositoryRoot, filepath.Join("deploy", "systemd", "system", storageViewWatchdogTimer))
	if err != nil {
		return fmt.Errorf("storage_watchdog_asset_invalid")
	}
	service = strings.NewReplacer(
		"__MOOX_HOME__", filepath.ToSlash(home),
		"__MOOX_STORAGE_ROOT__", serviceRoot,
		"__MOOX_USER__", user,
		"__MOOX_GROUP__", group,
		"__MOOX_EVENTBUS_URL__", eventBusURL,
		"__MOOX_GATEWAY_NODE_ID__", gatewayNodeID,
	).Replace(service)
	if strings.Contains(service, "__MOOX_") {
		return fmt.Errorf("storage_watchdog_asset_invalid")
	}

	token, err := newActivationToken()
	if err != nil {
		return fmt.Errorf("storage_watchdog_install_invalid")
	}
	tmpRoot := "/tmp/moox-storage-view-watchdog-" + token
	tmpScript := tmpRoot + ".sh"
	tmpService := tmpRoot + ".service"
	tmpTimer := tmpRoot + ".timer"
	cleanup := func() {
		_, _ = transport.Run(context.Background(), []string{"sh", "-lc", "unlink -- \"$1\" \"$2\" \"$3\" 2>/dev/null || true", "moox-watchdog-cleanup", tmpScript, tmpService, tmpTimer}, nil)
	}
	defer cleanup()

	for _, asset := range []struct {
		name string
		data string
		mode fs.FileMode
	}{
		{name: tmpScript, data: script, mode: 0o755},
		{name: tmpService, data: service, mode: 0o644},
		{name: tmpTimer, data: timer, mode: 0o644},
	} {
		if err := uploadWatchdogAsset(ctx, transport, asset.name, asset.data, asset.mode); err != nil {
			return fmt.Errorf("storage_watchdog_upload_failed")
		}
	}

	install := `set -eu
script="$1"
service="$2"
timer="$3"
sudo -n install -d -m 0755 /usr/local/libexec /etc/systemd/system
sudo -n install -o root -g root -m 0755 "$script" /usr/local/libexec/moox-storage-view-watchdog
sudo -n install -o root -g root -m 0644 "$service" /etc/systemd/system/moox-storage-view-watchdog.service
sudo -n install -o root -g root -m 0644 "$timer" /etc/systemd/system/moox-storage-view-watchdog.timer
sudo -n systemctl daemon-reload
sudo -n systemctl reset-failed moox-storage-view-watchdog.service || true
sudo -n systemctl enable moox-storage-view-watchdog.timer
# A timer whose previous schedule elapsed remains "active (elapsed)" and a
# plain start is a no-op. Restart always arms the newly installed schedule.
sudo -n systemctl restart moox-storage-view-watchdog.timer
sudo -n systemctl is-enabled --quiet moox-storage-view-watchdog.timer
sudo -n systemctl is-active --quiet moox-storage-view-watchdog.timer
armed=0
for _ in 1 2 3 4 5 6 7 8 9 10; do
  next=$(sudo -n systemctl show -p NextElapseUSecMonotonic --value moox-storage-view-watchdog.timer)
  if [ -n "$next" ] && [ "$next" != infinity ] && [ "$next" != 0 ]; then
    armed=1
    break
  fi
  sleep 1
done
[ "$armed" = 1 ] || {
  echo storage_watchdog_timer_not_armed >&2
  exit 1
}
`
	result, err := transport.Run(ctx, []string{"sh", "-lc", install, "moox-install-storage-watchdog", tmpScript, tmpService, tmpTimer}, nil)
	if err != nil {
		detail := strings.TrimSpace(strings.Join([]string{result.Stderr, result.Stdout}, "\n"))
		if len(detail) > 240 {
			detail = detail[len(detail)-240:]
		}
		if detail != "" {
			return fmt.Errorf("storage_watchdog_enable_failed: %s", strings.Join(strings.Fields(detail), " "))
		}
		return fmt.Errorf("storage_watchdog_enable_failed")
	}
	return nil
}

func remoteIdentity(ctx context.Context, transport setupssh.Client) (string, string, string, error) {
	result, err := transport.Run(ctx, []string{"sh", "-lc", `printf '%s\n%s\n%s\n' "$HOME" "$(id -un)" "$(id -gn)"`}, nil)
	if err != nil {
		return "", "", "", err
	}
	lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	if len(lines) != 3 || !filepath.IsAbs(lines[0]) || !validIdentityPart(lines[1]) || !validIdentityPart(lines[2]) {
		return "", "", "", fmt.Errorf("invalid remote identity")
	}
	return lines[0], lines[1], lines[2], nil
}

func validIdentityPart(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r) {
			continue
		}
		return false
	}
	return true
}

func readWatchdogAsset(root, relative string) (string, error) {
	path := filepath.Join(root, relative)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return "", fmt.Errorf("invalid watchdog asset")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func uploadWatchdogAsset(ctx context.Context, transport setupssh.Client, path, contents string, mode fs.FileMode) error {
	return transport.Upload(ctx, strings.NewReader(contents), int64(len(contents)), path, mode)
}
