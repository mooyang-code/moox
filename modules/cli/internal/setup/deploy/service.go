package deploy

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"strings"
	"time"

	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	trpc "trpc.group/trpc-go/trpc-go"
)

const (
	maxServicePackageSize      = 1 << 30
	maxServicePackageEntrySize = 512 << 20
)

type ServiceOptions struct {
	PackagePath string
	ServiceName string
	DeployDir   string
	// EventBusURL is supplied for Trade service preflight. A standalone
	// service package otherwise only has the module's loopback default, while
	// the deployed process receives the remote endpoint from start.sh.
	EventBusURL string
	// TradeConsoleBindAddress is applied only when Trade runs on a dedicated
	// node. TradeConsole is an unauthenticated internal service; dedicated
	// deployments must keep it on loopback and expose the authenticated
	// trade_owner route through Gateway/Caddy instead.
	TradeConsoleBindAddress string
}

type ServiceResult struct {
	ServiceName    string `json:"service_name"`
	DeployDir      string `json:"deploy_dir"`
	RemoteArchive  string `json:"remote_archive"`
	LocalSHA256    string `json:"local_sha256"`
	RemoteSHA256   string `json:"remote_sha256"`
	RegistrySynced bool   `json:"registry_synced,omitempty"`
}

func Service(ctx context.Context, transport setupssh.Client, opts ServiceOptions) (result ServiceResult, returnErr error) {
	if transport == nil || strings.TrimSpace(opts.PackagePath) == "" || !validReleaseToken(opts.ServiceName) {
		return result, fmt.Errorf("service_deploy_invalid")
	}
	if isTradeServiceName(opts.ServiceName) && strings.TrimSpace(opts.TradeConsoleBindAddress) != "" {
		ip := net.ParseIP(strings.TrimSpace(opts.TradeConsoleBindAddress))
		if ip == nil || !ip.IsLoopback() {
			return result, fmt.Errorf("service_deploy_invalid")
		}
	}
	packageSize, digest, err := inspectServicePackage(opts.PackagePath)
	if err != nil {
		return result, err
	}
	result.ServiceName = opts.ServiceName
	result.LocalSHA256 = digest

	deployDir, err := resolveRemoteDeployDir(ctx, transport, opts.DeployDir)
	if err != nil {
		return result, fmt.Errorf("service_deploy_invalid")
	}
	result.DeployDir = deployDir
	token, err := randomServiceToken()
	if err != nil {
		return result, fmt.Errorf("service_deploy_invalid")
	}
	result.RemoteArchive = "/tmp/moox-service-" + token + ".zip"

	file, err := os.Open(opts.PackagePath)
	if err != nil {
		return result, fmt.Errorf("service_package_invalid")
	}
	defer file.Close()
	if err := transport.Upload(ctx, file, packageSize, result.RemoteArchive, fs.FileMode(0o600)); err != nil {
		return result, fmt.Errorf("service_upload_failed")
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(trpc.BackgroundContext(), 10*time.Second)
		defer cancel()
		_, _ = transport.Run(cleanupCtx, []string{"rm", "-f", result.RemoteArchive}, nil)
	}()

	digestResult, err := transport.Run(ctx, []string{"sha256sum", result.RemoteArchive}, nil)
	if err != nil {
		return result, fmt.Errorf("service_digest_failed")
	}
	result.RemoteSHA256, err = parseSHA256(digestResult.Stdout)
	if err != nil {
		return result, fmt.Errorf("service_digest_failed")
	}
	if result.RemoteSHA256 != result.LocalSHA256 {
		return result, fmt.Errorf("service_digest_mismatch")
	}

	if _, err := transport.Run(ctx, []string{"bash", "-lc", prepareServiceScript, "moox-prepare-service", deployDir, opts.ServiceName, result.RemoteArchive, opts.EventBusURL, strings.TrimSpace(opts.TradeConsoleBindAddress)}, nil); err != nil {
		return result, fmt.Errorf("service_prepare_failed")
	}
	prepared := true
	defer func() {
		if returnErr != nil && prepared {
			rollbackCtx, cancel := context.WithTimeout(trpc.BackgroundContext(), 30*time.Second)
			defer cancel()
			_, _ = transport.Run(rollbackCtx, []string{"bash", "-lc", rollbackServiceScript, "moox-rollback-service", deployDir, opts.ServiceName}, nil)
		}
	}()

	if _, err := transport.Run(ctx, []string{"bash", "-lc", activateServiceScript, "moox-activate-service", deployDir, opts.ServiceName}, nil); err != nil {
		return result, fmt.Errorf("service_activate_failed")
	}
	// Keep .moox-service.previous until the caller has completed registry,
	// route, and placement checks. This lets post-activation failures restore
	// the previous binary/config instead of leaving an unregistered upgrade.
	prepared = false
	return result, nil
}

func isTradeServiceName(name string) bool {
	name = strings.TrimSpace(name)
	return strings.EqualFold(name, "trade") || strings.EqualFold(name, "moox_trade")
}

// FinalizeService commits a successfully activated deployment after all
// control-plane registration and route checks have passed.
func FinalizeService(ctx context.Context, transport setupssh.Client, deployDir string) error {
	if transport == nil || strings.TrimSpace(deployDir) == "" {
		return fmt.Errorf("service_finalize_invalid")
	}
	if _, err := transport.Run(ctx, []string{"bash", "-lc", finalizeServiceScript, "moox-finalize-service", deployDir}, nil); err != nil {
		return fmt.Errorf("service_finalize_failed")
	}
	return nil
}

// RollbackService restores the previous deployment retained by Service after
// an activation or post-activation validation failure.
func RollbackService(ctx context.Context, transport setupssh.Client, deployDir, service string) error {
	if transport == nil || strings.TrimSpace(deployDir) == "" || !validReleaseToken(service) {
		return fmt.Errorf("service_rollback_invalid")
	}
	if _, err := transport.Run(ctx, []string{"bash", "-lc", rollbackServiceScript, "moox-rollback-service", deployDir, service}, nil); err != nil {
		return fmt.Errorf("service_rollback_failed")
	}
	return nil
}

func inspectServicePackage(packagePath string) (int64, string, error) {
	info, err := os.Stat(packagePath)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > maxServicePackageSize {
		return 0, "", fmt.Errorf("service_package_invalid")
	}
	file, err := os.Open(packagePath)
	if err != nil {
		return 0, "", fmt.Errorf("service_package_invalid")
	}
	defer file.Close()
	reader, err := zip.NewReader(file, info.Size())
	if err != nil {
		return 0, "", fmt.Errorf("service_package_invalid")
	}
	seen := make(map[string]struct{}, len(reader.File))
	var uncompressedSize uint64
	hasBinary, hasConfig := false, false
	hasStart, hasStop, hasHealth := false, false, false
	for _, entry := range reader.File {
		name := entry.Name
		isDirectory := strings.HasSuffix(name, "/")
		normalized := strings.TrimSuffix(name, "/")
		if !validServiceEntry(normalized) || strings.ContainsAny(name, "\x00\r\n\t\\") {
			return 0, "", fmt.Errorf("service_package_invalid")
		}
		if _, exists := seen[normalized]; exists {
			return 0, "", fmt.Errorf("service_package_invalid")
		}
		seen[normalized] = struct{}{}
		if entry.Mode()&os.ModeSymlink != 0 {
			return 0, "", fmt.Errorf("service_package_invalid")
		}
		if entry.UncompressedSize64 > maxServicePackageEntrySize {
			return 0, "", fmt.Errorf("service_package_invalid")
		}
		uncompressedSize += entry.UncompressedSize64
		if uncompressedSize > maxServicePackageSize {
			return 0, "", fmt.Errorf("service_package_invalid")
		}
		if isDirectory {
			continue
		}
		switch {
		case normalized == "start.sh":
			hasStart = true
		case normalized == "stop.sh":
			hasStop = true
		case normalized == "healthcheck.sh":
			hasHealth = true
		case strings.HasPrefix(normalized, "bin/"):
			hasBinary = true
		case strings.HasPrefix(normalized, "config/"):
			hasConfig = true
		}
	}
	if len(reader.File) == 0 || !hasBinary || !hasConfig || !hasStart || !hasStop || !hasHealth {
		return 0, "", fmt.Errorf("service_package_invalid")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return 0, "", fmt.Errorf("service_package_invalid")
	}
	digest, err := sha256File(file)
	if err != nil {
		return 0, "", fmt.Errorf("service_package_invalid")
	}
	return info.Size(), digest, nil
}

func validServiceEntry(name string) bool {
	if name == "" || name == "." || name == ".." || path.IsAbs(name) || path.Clean(name) != name {
		return false
	}
	for _, blocked := range []string{"data", "logs", "run", "secrets", "certs"} {
		if name == blocked || strings.HasPrefix(name, blocked+"/") {
			return false
		}
	}
	return true
}

func randomServiceToken() (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

const prepareServiceScript = `set -eu
deploy=$1
service=$2
archive=$3
eventbus_url=${4:-}
trade_console_bind=${5:-}
stage="$deploy/.moox-service.next"
previous="$deploy/.moox-service.previous"
manifest="$previous/manifest"
rm -rf -- "$stage"
mkdir -p -- "$stage"
command -v unzip >/dev/null 2>&1
unzip -q -- "$archive" -d "$stage"
test -f "$stage/start.sh"
test -f "$stage/stop.sh"
test -f "$stage/healthcheck.sh"
test -d "$stage/bin"
test -d "$stage/config"
chmod +x "$stage/start.sh" "$stage/stop.sh" "$stage/healthcheck.sh" "$stage/bin/"*
while IFS= read -r entry; do
  case "$entry" in
    ''|data|data/*|logs|logs/*|run|run/*|secrets|secrets/*|certs|certs/*|/*|../*|*/../*|*'/../'*|*'\\'*|*'	'*) exit 1 ;;
  esac
done < <(unzip -Z1 -- "$archive")
if [ "$service" = "trade" ] || [ "$service" = "moox_trade" ]; then
  [ -x "$stage/bin/moox-trade-cli" ] || {
    echo "trade_eventbus_preflight_binary_missing" >&2
    exit 1
  }
  trade_config="$stage/config/app.yaml"
  [ -f "$trade_config" ] || trade_config="$stage/trade/config/app.yaml"
  [ -f "$trade_config" ] || {
    echo "trade_eventbus_preflight_config_missing" >&2
    exit 1
  }
if [ -n "$trade_console_bind" ]; then
    trpc_config="$stage/config/trpc_go.yaml"
    [ -f "$trpc_config" ] || trpc_config="$stage/trade/config/trpc_go.yaml"
    [ -f "$trpc_config" ] || {
      echo "trade_console_bind_config_missing" >&2
      exit 1
    }
    TRADE_CONSOLE_BIND="$trade_console_bind" python3 - "$trpc_config" <<'PY'
import ipaddress
import os
import pathlib
import re
import sys

path = pathlib.Path(sys.argv[1])
bind = os.environ["TRADE_CONSOLE_BIND"]
ip = ipaddress.ip_address(bind)
if not ip.is_loopback:
    raise SystemExit("trade_console_bind must be a loopback address")
raw = path.read_text(encoding="utf-8")
pattern = re.compile(r"(?ms)(^[ \t]*- name:[ \t]*trpc\.moox\.trade\.TradeConsoleService[ \t]*\n.*?^[ \t]+ip:[ \t]*)[^\r\n]+")
updated, count = pattern.subn(lambda match: match.group(1) + bind, raw, count=1)
if count != 1:
    raise SystemExit("TradeConsoleService listener not found")
path.write_text(updated, encoding="utf-8")
PY
  fi
  if [ -n "$eventbus_url" ]; then
    MOOX_EVENTBUS_NATS_URL="$eventbus_url" "$stage/bin/moox-trade-cli" eventbus-check --config "$trade_config"
  else
    "$stage/bin/moox-trade-cli" eventbus-check --config "$trade_config"
  fi || {
    echo "trade_eventbus_preflight_failed" >&2
    exit 1
  }
fi
if [ -x "$deploy/stop.sh" ]; then
  if ! "$deploy/stop.sh" "$service"; then
    "$deploy/start.sh" "$service" >/dev/null 2>&1 || true
    rm -rf -- "$stage"
    exit 1
  fi
fi
rm -rf -- "$previous"
mkdir -p -- "$previous"
if [ "$service" = "trade" ] || [ "$service" = "moox_trade" ]; then
  # Trade schema migrations run during init/startup. Keep the stopped
  # SQLite database (including WAL/SHM) outside the package manifest so a
  # registry or readiness failure can restore the exact pre-upgrade schema.
  trade_config_path="$deploy/config/app.yaml"
  trade_work_dir="$deploy"
  if [ ! -f "$trade_config_path" ] && [ -f "$deploy/trade/config/app.yaml" ]; then
    trade_config_path="$deploy/trade/config/app.yaml"
    trade_work_dir="$deploy/trade"
  fi
  db_rel=$(python3 - "$trade_config_path" <<'PY'
import pathlib
import re
import sys

path = pathlib.Path(sys.argv[1])
try:
    raw = path.read_text(encoding="utf-8")
except OSError:
    raw = ""
match = re.search(r"(?ms)^\s*database:\s*\n\s+path:\s*['\"]?([^#\r\n'\"]+)", raw)
print(match.group(1).strip() if match else "./data/moox_trade.db")
PY
)
  case "$db_rel" in
    /*) db_path="$db_rel" ;;
    *) db_path="$trade_work_dir/$db_rel" ;;
  esac
  db_backup="$previous/database-backup"
  mkdir -p -- "$db_backup"
  printf '%s\n' "$db_path" >"$previous/database-path"
  for suffix in "" "-wal" "-shm"; do
    if [ -e "$db_path$suffix" ] || [ -L "$db_path$suffix" ]; then
      cp -a -- "$db_path$suffix" "$db_backup/$(basename "$db_path$suffix")"
    fi
  done
fi
while IFS= read -r entry; do
  case "$entry" in ''|*/|data|data/*|logs|logs/*|run|run/*|secrets|secrets/*|certs|certs/*) continue ;; esac
  target="$deploy/$entry"
  if [ -e "$target" ] || [ -L "$target" ]; then
    mkdir -p -- "$previous/$(dirname "$entry")"
    cp -a -- "$target" "$previous/$entry"
    printf 'existing\t%s\n' "$entry" >>"$manifest"
  else
    printf 'new\t%s\n' "$entry" >>"$manifest"
  fi
  rm -rf -- "$target"
  mkdir -p -- "$(dirname "$target")"
  cp -a -- "$stage/$entry" "$target"
done < <(unzip -Z1 -- "$archive")
`

const activateServiceScript = `set -eu
deploy=$1
service=$2
"$deploy/start.sh" "$service"
"$deploy/healthcheck.sh" "$service"
`

const rollbackServiceScript = `set -eu
deploy=$1
service=$2
previous="$deploy/.moox-service.previous"
manifest="$previous/manifest"
if [ ! -f "$manifest" ]; then
  rm -rf -- "$deploy/.moox-service.next" "$previous"
  exit 0
fi
"$deploy/stop.sh" "$service" >/dev/null 2>&1 || true
while IFS="$(printf '\t')" read -r kind entry; do
  [ -n "$entry" ] || continue
  target="$deploy/$entry"
  rm -rf -- "$target"
  if [ "$kind" = existing ]; then
    mkdir -p -- "$(dirname "$target")"
    cp -a -- "$previous/$entry" "$target"
  fi
done <"$manifest"
if [ "$service" = "trade" ] || [ "$service" = "moox_trade" ]; then
  # Restore the database before starting the previous binary. Without this,
  # an init_schema migration would leave the old binary unable to open the
  # expanded schema after a post-activation rollback.
  if [ -f "$previous/database-path" ]; then
    db_path=$(cat "$previous/database-path")
    db_backup="$previous/database-backup"
    for suffix in "" "-wal" "-shm"; do
      rm -f -- "$db_path$suffix"
      backup_file="$db_backup/$(basename "$db_path$suffix")"
      if [ -e "$backup_file" ] || [ -L "$backup_file" ]; then
        mkdir -p -- "$(dirname "$db_path$suffix")"
        cp -a -- "$backup_file" "$db_path$suffix"
      fi
    done
  fi
fi
# Do not discard the recovery snapshot until the restored version has started
# and passed its service health check. A failed rollback must remain retryable
# and must be surfaced to the caller instead of silently leaving Trade down.
"$deploy/start.sh" "$service"
"$deploy/healthcheck.sh" "$service"
rm -rf -- "$deploy/.moox-service.next" "$previous"
`

const finalizeServiceScript = `set -eu
deploy=$1
rm -rf -- "$deploy/.moox-service.next" "$deploy/.moox-service.previous"
`
