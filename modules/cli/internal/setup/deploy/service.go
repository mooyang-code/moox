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
	// node. Dedicated cloud hosts may expose a public SSH address while the
	// process only sees a private interface, so the deployment uses 0.0.0.0
	// and relies on the cloud firewall to limit port 11200 to the control host.
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
	if strings.EqualFold(strings.TrimSpace(opts.ServiceName), "trade") && strings.TrimSpace(opts.TradeConsoleBindAddress) != "" {
		ip := net.ParseIP(strings.TrimSpace(opts.TradeConsoleBindAddress))
		if ip == nil || ip.IsLoopback() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || (ip.IsUnspecified() && strings.TrimSpace(opts.TradeConsoleBindAddress) != "0.0.0.0") {
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
	if _, err := transport.Run(ctx, []string{"bash", "-lc", finalizeServiceScript, "moox-finalize-service", deployDir}, nil); err != nil {
		return result, fmt.Errorf("service_finalize_failed")
	}
	prepared = false
	return result, nil
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
rm -rf -- "$stage" "$previous"
mkdir -p -- "$stage" "$previous"
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
if [ "$service" = "trade" ]; then
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
if ip.is_loopback or ip.is_multicast or ip.is_link_local or (ip.is_unspecified and bind != "0.0.0.0"):
    raise SystemExit("trade_console_bind must be 0.0.0.0 or a routable unicast IP")
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
    rm -rf -- "$stage" "$previous"
    exit 1
  fi
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
while IFS="$(printf '\\t')" read -r kind entry; do
  [ -n "$entry" ] || continue
  target="$deploy/$entry"
  rm -rf -- "$target"
  if [ "$kind" = existing ]; then
    mkdir -p -- "$(dirname "$target")"
    cp -a -- "$previous/$entry" "$target"
  fi
done <"$manifest"
rm -rf -- "$deploy/.moox-service.next" "$previous"
"$deploy/start.sh" "$service" >/dev/null 2>&1 || true
`

const finalizeServiceScript = `set -eu
deploy=$1
rm -rf -- "$deploy/.moox-service.next" "$deploy/.moox-service.previous"
`
