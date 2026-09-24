package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/spf13/cobra"
)

type storageRebuildSummary struct {
	Status         string                      `json:"status"`
	Host           string                      `json:"host"`
	StorageRoot    string                      `json:"storage_root"`
	DryRun         bool                        `json:"dry_run"`
	Quiesced       []string                    `json:"quiesced,omitempty"`
	Reset          storageRebuildResetSummary  `json:"reset"`
	ViewReset      *storageRebuildResetSummary `json:"view_reset,omitempty"`
	Initialization *setupInitSummary           `json:"initialization,omitempty"`
	Verification   *storageVerifyResult        `json:"verification,omitempty"`
}

type storageRebuildResetSummary struct {
	Stream             string   `json:"stream,omitempty"`
	Consumers          []string `json:"consumers,omitempty"`
	Views              int      `json:"views,omitempty"`
	RecordViews        int      `json:"record_views,omitempty"`
	Indexes            []string `json:"indexes,omitempty"`
	PrimaryDataRemoved bool     `json:"primary_data_removed"`
	QueuePurged        bool     `json:"queue_purged"`
	ViewMetadataReset  bool     `json:"view_metadata_reset"`
	DryRun             bool     `json:"dry_run"`
	Lookback           string   `json:"rebuild_lookback,omitempty"`
}

type storageResetOperationResult struct {
	Module  string                     `json:"module"`
	Action  string                     `json:"action"`
	Status  string                     `json:"status"`
	Summary storageRebuildResetSummary `json:"summary"`
}

func newSetupRebuildStorageCommand(deps setupDeps) *cobra.Command {
	var file, configDir, host string
	var yes bool
	cmd := &cobra.Command{
		Use:   "rebuild-storage",
		Short: "全量清理并重建 Storage、View 和元数据",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			storageHost, err := findSetupHost(snapshot.Manifest, host)
			if err != nil {
				return err
			}
			validation, validationErr := deps.validateDeployment(cmd.Context(), snapshot, []setupconfig.Host{snapshot.Manifest.ControlHost, storageHost})
			if validationErr != nil {
				if encodeErr := writeSetupJSON(cmd, validation); encodeErr != nil {
					return encodeErr
				}
				return validationErr
			}
			status, err := deps.status(cmd.Context(), snapshot)
			if err != nil {
				return err
			}
			if status.State != "completed" {
				return fmt.Errorf("setup_incomplete")
			}
			if _, err := deps.loadInitBundle(configDir); err != nil {
				return err
			}
			result, err := deps.rebuildStorage(cmd.Context(), snapshot, host, file, configDir, yes)
			if err != nil {
				return err
			}
			if err := snapshot.VerifyUnchanged(); err != nil {
				return fmt.Errorf("config_changed")
			}
			return writeSetupJSON(cmd, result)
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&configDir, "config-dir", defaultSetupConfigDir, "默认配置目录")
	cmd.Flags().StringVar(&host, "host", "", "Storage 目标主机名称")
	cmd.Flags().BoolVar(&yes, "yes", false, "确认删除全部 Storage、View、消费者和元数据")
	_ = cmd.MarkFlagRequired("host")
	return cmd
}

func runSetupRebuildStorage(
	ctx context.Context,
	deps setupDeps,
	snapshot *setupconfig.Snapshot,
	host, file, configDir string,
	apply bool,
) (summary storageRebuildSummary, retErr error) {
	if snapshot == nil {
		return storageRebuildSummary{}, errors.New("storage_rebuild_invalid")
	}
	storageHost, err := resolveStorageDeploymentHost(snapshot.Manifest, host)
	if err != nil {
		return storageRebuildSummary{}, err
	}
	storageRoot := snapshot.Manifest.Paths.Resolved().StorageRoot
	summary = storageRebuildSummary{Status: "dry_run", Host: storageHost.Name, StorageRoot: storageRoot, DryRun: !apply}
	reset, err := defaultSetupRebuildStorageRemote(ctx, snapshot, storageHost.Name, false)
	if err != nil {
		return summary, err
	}
	summary.Reset = reset
	if !apply {
		return summary, nil
	}

	stopped, err := quiesceStorageWriters(ctx, snapshot)
	if err != nil {
		return summary, err
	}
	summary.Quiesced = stopped
	defer func() {
		if resumeErr := resumeStorageWriters(context.Background(), snapshot, stopped); resumeErr != nil && retErr == nil {
			retErr = resumeErr
		}
	}()

	summary.Reset, err = defaultSetupRebuildStorageRemote(ctx, snapshot, storageHost.Name, true)
	if err != nil {
		return summary, err
	}
	bundle, err := deps.loadInitBundle(configDir)
	if err != nil {
		return summary, err
	}
	initialized, err := deps.initStorage(ctx, snapshot, file, configDir, storageHost.Name, bundle)
	if err != nil {
		return summary, err
	}
	summary.Initialization = &initialized
	viewReset, err := defaultSetupRebuildStorageViewsRemote(ctx, snapshot, storageHost.Name)
	if err != nil {
		return summary, err
	}
	summary.ViewReset = &viewReset
	verification, err := deps.verifyStorage(ctx, snapshot, storageHost.Name)
	if err != nil {
		return summary, err
	}
	summary.Verification = &verification
	summary.Status = "ready"
	summary.DryRun = false
	return summary, nil
}

func defaultSetupRebuildStorageViewsRemote(ctx context.Context, snapshot *setupconfig.Snapshot, hostName string) (storageRebuildResetSummary, error) {
	host, err := resolveStorageDeploymentHost(snapshot.Manifest, hostName)
	if err != nil {
		return storageRebuildResetSummary{}, err
	}
	transport, err := dialSetupHost(ctx, host)
	if err != nil {
		return storageRebuildResetSummary{}, err
	}
	defer transport.Close()
	root := snapshot.Manifest.Paths.Resolved().StorageRoot
	eventBusURL, err := storageRebuildEventBusURL(snapshot)
	if err != nil {
		return storageRebuildResetSummary{}, err
	}
	result, err := transport.Run(ctx, []string{"sh", "-lc", storageRebuildFinalizeViewsScript, "moox-rebuild-storage-views", root, eventBusURL}, nil)
	if err != nil {
		detail := strings.TrimSpace(result.Stderr)
		if detail == "" {
			detail = strings.TrimSpace(result.Stdout)
		}
		if len(detail) > 600 {
			detail = detail[len(detail)-600:]
		}
		if detail != "" {
			return storageRebuildResetSummary{}, fmt.Errorf("storage_rebuild_view_reset_failed: %s", strings.Join(strings.Fields(detail), " "))
		}
		return storageRebuildResetSummary{}, fmt.Errorf("storage_rebuild_view_reset_failed: %w", err)
	}
	parsed, err := parseStorageResetOperationResult(result.Stdout)
	if err != nil {
		return storageRebuildResetSummary{}, err
	}
	if parsed.Status != "ok" || !parsed.Summary.QueuePurged || !parsed.Summary.ViewMetadataReset {
		return storageRebuildResetSummary{}, fmt.Errorf("storage_rebuild_view_reset_incomplete")
	}
	return parsed.Summary, nil
}

func defaultSetupRebuildStorageRemote(ctx context.Context, snapshot *setupconfig.Snapshot, hostName string, apply bool) (storageRebuildResetSummary, error) {
	host, err := resolveStorageDeploymentHost(snapshot.Manifest, hostName)
	if err != nil {
		return storageRebuildResetSummary{}, err
	}
	transport, err := dialSetupHost(ctx, host)
	if err != nil {
		return storageRebuildResetSummary{}, err
	}
	defer transport.Close()
	root := snapshot.Manifest.Paths.Resolved().StorageRoot
	eventBusURL, err := storageRebuildEventBusURL(snapshot)
	if err != nil {
		return storageRebuildResetSummary{}, err
	}
	mode := "--dry-run"
	if apply {
		mode = "--yes"
	}
	result, err := transport.Run(ctx, []string{"sh", "-lc", storageRebuildRemoteScript, "moox-rebuild-storage", root, mode, eventBusURL}, nil)
	if err != nil {
		detail := strings.TrimSpace(result.Stderr)
		if detail == "" {
			detail = strings.TrimSpace(result.Stdout)
		}
		if len(detail) > 600 {
			detail = detail[len(detail)-600:]
		}
		if detail != "" {
			return storageRebuildResetSummary{}, fmt.Errorf("storage_rebuild_remote_failed: %s", strings.Join(strings.Fields(detail), " "))
		}
		return storageRebuildResetSummary{}, fmt.Errorf("storage_rebuild_remote_failed: %w", err)
	}
	parsed, err := parseStorageResetOperationResult(result.Stdout)
	if err != nil {
		return storageRebuildResetSummary{}, err
	}
	if parsed.Status != "dry_run" && parsed.Status != "ok" {
		return storageRebuildResetSummary{}, fmt.Errorf("storage_rebuild_remote_status_invalid: %s", parsed.Status)
	}
	if apply && (!parsed.Summary.PrimaryDataRemoved || !parsed.Summary.QueuePurged || !parsed.Summary.ViewMetadataReset) {
		return storageRebuildResetSummary{}, fmt.Errorf("storage_rebuild_remote_incomplete")
	}
	return parsed.Summary, nil
}

func storageRebuildEventBusURL(snapshot *setupconfig.Snapshot) (string, error) {
	if snapshot == nil {
		return "", errors.New("storage_rebuild_eventbus_invalid")
	}
	address := strings.TrimSpace(snapshot.Manifest.EventBus.PublicAddress)
	port := snapshot.Manifest.EventBus.Port
	if address == "" || port <= 0 {
		return "", errors.New("storage_rebuild_eventbus_endpoint_missing")
	}
	scheme := "nats"
	if snapshot.Manifest.EventBus.TLSEnabled {
		scheme = "tls"
	}
	return scheme + "://" + net.JoinHostPort(address, strconv.Itoa(port)), nil
}

func parseStorageResetOperationResult(raw string) (storageResetOperationResult, error) {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var result storageResetOperationResult
		if err := json.Unmarshal([]byte(line), &result); err == nil && result.Action == "reset-view-consumers" {
			return result, nil
		}
	}
	return storageResetOperationResult{}, errors.New("storage_rebuild_remote_result_invalid")
}

func quiesceStorageWriters(ctx context.Context, snapshot *setupconfig.Snapshot) ([]string, error) {
	control, err := dialSetupHost(ctx, snapshot.Manifest.ControlHost)
	if err != nil {
		return nil, fmt.Errorf("storage_rebuild_control_unreachable: %w", err)
	}
	defer control.Close()
	root := snapshot.Manifest.Paths.Resolved().ControlRoot
	result, err := control.Run(ctx, []string{"sh", "-lc", storageRebuildQuiesceScript, "moox-rebuild-storage-quiesce", root}, nil)
	var stopped []string
	for _, line := range strings.Split(result.Stdout, "\n") {
		if service := strings.TrimSpace(line); service != "" {
			stopped = append(stopped, service)
		}
	}
	if err != nil {
		_ = resumeStorageWriters(context.Background(), snapshot, stopped)
		return nil, fmt.Errorf("storage_rebuild_quiesce_failed: %w", err)
	}
	return stopped, nil
}

func resumeStorageWriters(ctx context.Context, snapshot *setupconfig.Snapshot, services []string) error {
	control, err := dialSetupHost(ctx, snapshot.Manifest.ControlHost)
	if err != nil {
		return fmt.Errorf("storage_rebuild_resume_unreachable: %w", err)
	}
	defer control.Close()
	root := snapshot.Manifest.Paths.Resolved().ControlRoot
	args := []string{"sh", "-lc", storageRebuildResumeScript, "moox-rebuild-storage-resume", root}
	args = append(args, services...)
	result, err := control.Run(ctx, args, nil)
	if err != nil {
		detail := strings.TrimSpace(result.Stderr)
		if detail != "" {
			return fmt.Errorf("storage_rebuild_resume_failed: %s", strings.Join(strings.Fields(detail), " "))
		}
		return fmt.Errorf("storage_rebuild_resume_failed: %w", err)
	}
	return nil
}

const storageRebuildQuiesceScript = `set -eu
root="$1"
test -x "$root/stop.sh"
umask 077
state="$root/run/storage-rebuild-healthchecks.crontab"
mkdir -p "$root/run"
crontab_command="${MOOX_CRONTAB_COMMAND:-crontab}"
current=""
if command -v "$crontab_command" >/dev/null 2>&1; then
  current=$("$crontab_command" -l 2>/dev/null || true)
  if printf '%s\n' "$current" | grep -Fq '# moox-healthchecks'; then
    printf '%s\n' "$current" >"$state.tmp.$$"
    mv -f -- "$state.tmp.$$" "$state"
    printf '%s\n' "$current" | grep -Fv '# moox-healthchecks' | "$crontab_command" -
  fi
fi
for service in collector factor strategy trade; do
  if pgrep -f -- "$root/bin/moox-$service([[:space:]]|$)" >/dev/null 2>&1; then
    "$root/stop.sh" "$service"
    printf '%s\n' "$service"
  fi
done
`

const storageRebuildResumeScript = `set -eu
root="$1"
shift
umask 077
state="$root/run/storage-rebuild-healthchecks.crontab"
crontab_command="${MOOX_CRONTAB_COMMAND:-crontab}"
restore_healthchecks() {
  if [ -f "$state" ]; then
    command -v "$crontab_command" >/dev/null 2>&1 || {
      echo storage_rebuild_healthchecks_restore_unavailable >&2
      return 1
    }
    "$crontab_command" <"$state"
    rm -f -- "$state"
  fi
}
trap restore_healthchecks EXIT
test -x "$root/start.sh"
for service in "$@"; do
  case "$service" in
    collector|factor|strategy|trade) "$root/start.sh" "$service" ;;
    *) echo "storage_rebuild_service_invalid" >&2; exit 1 ;;
  esac
done
`

const storageRebuildRemoteScript = `set -eu
root="$1"
mode="$2"
eventbus_url="$3"
case "$root" in
  /*) ;;
  *) echo storage_rebuild_root_invalid >&2; exit 1 ;;
esac
case "$mode" in
  --dry-run|--yes) ;;
  *) echo storage_rebuild_mode_invalid >&2; exit 1 ;;
esac
test -x "$root/bin/moox-storage-cli"
test -x "$root/start.sh"
test -x "$root/stop.sh"
test -f "$root/storage/config/storage.yaml"
test -r "$HOME/.config/moox/eventbus/internal-admin.yaml"
test -n "$eventbus_url"
if [ "$mode" = "--dry-run" ]; then
  "$root/bin/moox-storage-cli" reset-view-consumers \
    --storage-conf "$root/storage/config/storage.yaml" \
    --package-root "$root" \
    --credential-file "$HOME/.config/moox/eventbus/internal-admin.yaml" \
    --eventbus-url "$eventbus_url" \
    --timeout 30m --restart=false --reset-all-storage-data --dry-run
  exit 0
fi
umask 077
healthcheck_state="$root/run/storage-rebuild-healthchecks.crontab"
mkdir -p "$root/run"
healthcheck_crontab_command="${MOOX_CRONTAB_COMMAND:-crontab}"
restore_healthchecks() {
  if [ -f "$healthcheck_state" ]; then
    command -v "$healthcheck_crontab_command" >/dev/null 2>&1 || {
      echo storage_rebuild_healthchecks_restore_unavailable >&2
      return 1
    }
    "$healthcheck_crontab_command" <"$healthcheck_state"
    rm -f -- "$healthcheck_state"
  fi
}
trap restore_healthchecks EXIT
if command -v "$healthcheck_crontab_command" >/dev/null 2>&1; then
  healthcheck_current=$("$healthcheck_crontab_command" -l 2>/dev/null || true)
  if printf '%s\n' "$healthcheck_current" | grep -Fq '# moox-healthchecks'; then
    printf '%s\n' "$healthcheck_current" >"$healthcheck_state.tmp.$$"
    mv -f -- "$healthcheck_state.tmp.$$" "$healthcheck_state"
    printf '%s\n' "$healthcheck_current" | grep -Fv '# moox-healthchecks' | "$healthcheck_crontab_command" -
  fi
fi
"$root/bin/moox-storage-cli" reset-view-consumers \
  --storage-conf "$root/storage/config/storage.yaml" \
  --package-root "$root" \
  --credential-file "$HOME/.config/moox/eventbus/internal-admin.yaml" \
  --eventbus-url "$eventbus_url" \
  --timeout 30m --restart=false --reset-all-storage-data --yes
for path in "$root/data/storage" "$root/data/storage-node"; do
  case "$path" in
    "$root"/data/storage|"$root"/data/storage-node) ;;
    *) echo storage_rebuild_path_invalid >&2; exit 1 ;;
  esac
  rm -rf -- "$path"
done
mkdir -p "$root/data/storage" "$root/data/storage-node"
"$root/start.sh" storage
ready=0
for _ in $(seq 1 180); do
  if pgrep -f -- "$root/bin/moox-storage-primary([[:space:]]|$)" >/dev/null 2>&1 && \
     pgrep -f -- "$root/bin/moox-storage-view([[:space:]]|$)" >/dev/null 2>&1 && \
     pgrep -f -- "$root/bin/moox-storage-node([[:space:]]|$)" >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 2
done
if [ "$ready" != 1 ]; then
  echo storage_rebuild_not_ready >&2
  exit 1
fi
`

const storageRebuildFinalizeViewsScript = `set -eu
root="$1"
eventbus_url="$2"
case "$root" in
  /*) ;;
  *) echo storage_rebuild_root_invalid >&2; exit 1 ;;
esac
test -x "$root/bin/moox-storage-cli"
test -x "$root/start.sh"
test -x "$root/stop.sh"
test -f "$root/storage/config/storage.yaml"
test -r "$HOME/.config/moox/eventbus/internal-admin.yaml"
test -n "$eventbus_url"
umask 077
healthcheck_state="$root/run/storage-rebuild-view-healthchecks.crontab"
mkdir -p "$root/run"
healthcheck_crontab_command="${MOOX_CRONTAB_COMMAND:-crontab}"
restore_healthchecks() {
  if [ -f "$healthcheck_state" ]; then
    command -v "$healthcheck_crontab_command" >/dev/null 2>&1 || {
      echo storage_rebuild_healthchecks_restore_unavailable >&2
      return 1
    }
    "$healthcheck_crontab_command" <"$healthcheck_state"
    rm -f -- "$healthcheck_state"
  fi
}
trap restore_healthchecks EXIT
if command -v "$healthcheck_crontab_command" >/dev/null 2>&1; then
  healthcheck_current=$("$healthcheck_crontab_command" -l 2>/dev/null || true)
  if printf '%s\n' "$healthcheck_current" | grep -Fq '# moox-healthchecks'; then
    printf '%s\n' "$healthcheck_current" >"$healthcheck_state.tmp.$$"
    mv -f -- "$healthcheck_state.tmp.$$" "$healthcheck_state"
    printf '%s\n' "$healthcheck_current" | grep -Fv '# moox-healthchecks' | "$healthcheck_crontab_command" -
  fi
fi
"$root/bin/moox-storage-cli" reset-view-consumers \
  --storage-conf "$root/storage/config/storage.yaml" \
  --package-root "$root" \
  --credential-file "$HOME/.config/moox/eventbus/internal-admin.yaml" \
  --eventbus-url "$eventbus_url" \
  --timeout 30m --restart=false --yes
"$root/start.sh" storage
ready=0
for _ in $(seq 1 180); do
  if pgrep -f -- "$root/bin/moox-storage-primary([[:space:]]|$)" >/dev/null 2>&1 && \
     pgrep -f -- "$root/bin/moox-storage-view([[:space:]]|$)" >/dev/null 2>&1 && \
     pgrep -f -- "$root/bin/moox-storage-node([[:space:]]|$)" >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 2
done
if [ "$ready" != 1 ]; then
  echo storage_rebuild_view_reset_not_ready >&2
  exit 1
fi
`
