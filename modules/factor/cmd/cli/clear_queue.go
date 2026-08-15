package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/jetstream"
)

type clearQueueSummary struct {
	Stream     string `json:"stream"`
	Consumer   string `json:"consumer"`
	Deleted    bool   `json:"deleted"`
	Pending    uint64 `json:"pending_before"`
	AckPending int    `json:"ack_pending_before"`
	Restarted  bool   `json:"restarted"`
	DryRun     bool   `json:"dry_run"`
}

var (
	deleteFactorQueueConsumer = deleteFactorQueueConsumerRemote
	runFactorLifecycle        = runFactorLifecycleScript
)

func runClearQueue(ctx context.Context, cfg cliConfig, out io.Writer) error {
	if err := validateClearQueueConfig(&cfg); err != nil {
		return err
	}
	if cfg.DryRun {
		return json.NewEncoder(out).Encode(map[string]any{
			"ok": true, "module": "factor", "action": "clear-queue", "status": "dry_run",
			"summary": clearQueueSummary{Stream: cfg.Stream, Consumer: cfg.Consumer, DryRun: true},
		})
	}
	if !cfg.Yes {
		return errors.New("clear-queue deletes the durable Factor consumer; re-run with --yes, or use --dry-run")
	}

	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	packageRoot := resolveFactorPackageRoot(cfg.PackageRoot)
	stopped := false
	started := false
	if cfg.Restart {
		if err := runFactorLifecycle(ctx, packageRoot, "stop"); err != nil {
			return fmt.Errorf("stop Factor: %w", err)
		}
		stopped = true
		defer func() {
			if stopped && !started {
				restartCtx, restartCancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer restartCancel()
				_ = runFactorLifecycle(restartCtx, packageRoot, "start")
			}
		}()
	}

	summary, err := deleteFactorQueueConsumer(ctx, factorQueueConsumerOptions{
		Stream: cfg.Stream, Consumer: cfg.Consumer,
		CredentialFile: cfg.CredentialFile, EventBusURL: cfg.EventBusURL,
	})
	if err != nil {
		return fmt.Errorf("clear Factor consumer: %w", err)
	}
	if cfg.Restart {
		if err := runFactorLifecycle(ctx, packageRoot, "start"); err != nil {
			return fmt.Errorf("start Factor: %w", err)
		}
		started = true
		summary.Restarted = true
	}
	return json.NewEncoder(out).Encode(map[string]any{
		"ok": true, "module": "factor", "action": "clear-queue", "status": "ok", "summary": summary,
	})
}

type factorQueueConsumerOptions struct {
	Stream, Consumer, CredentialFile, EventBusURL string
}

func validateClearQueueConfig(cfg *cliConfig) error {
	if strings.TrimSpace(cfg.Stream) == "" || strings.TrimSpace(cfg.Consumer) == "" {
		return errors.New("--stream and --consumer must not be empty")
	}
	if cfg.Timeout <= 0 {
		return errors.New("--timeout must be positive")
	}
	return nil
}

func deleteFactorQueueConsumerRemote(ctx context.Context, opts factorQueueConsumerOptions) (clearQueueSummary, error) {
	credentialPath := resolveFactorCredentialFile(opts.CredentialFile)
	if credentialPath == "" {
		return clearQueueSummary{}, errors.New("NATS admin credential is required; pass --credential-file or MOOX_EVENTBUS_INTERNAL_ADMIN_CREDENTIAL_FILE")
	}
	cfg := jetstream.ConfigFromEnv([]string{"tls://127.0.0.1:4222"}, "moox-factor-cli-clear-queue")
	// The operator credential file is the sole authentication source for this
	// destructive command; do not let a service credential from the environment
	// conflict with it.
	cfg.Username, cfg.Password, cfg.Credentials = "", "", ""
	if err := cfg.ApplyCredentialFile(credentialPath); err != nil {
		return clearQueueSummary{}, err
	}
	if strings.TrimSpace(opts.EventBusURL) != "" {
		cfg.URLs = []string{strings.TrimSpace(opts.EventBusURL)}
	}
	client, err := jetstream.Connect(ctx, cfg)
	if err != nil {
		return clearQueueSummary{}, err
	}
	defer client.Close()
	summary := clearQueueSummary{Stream: opts.Stream, Consumer: opts.Consumer}
	state, err := client.ConsumerState(ctx, opts.Stream, opts.Consumer)
	if errors.Is(err, jetstream.ErrConsumerNotFound) {
		return summary, nil
	}
	if err != nil {
		return summary, err
	}
	summary.Pending = state.NumPending
	summary.AckPending = state.NumAckPending
	if err := client.DeleteConsumer(ctx, opts.Stream, opts.Consumer); err != nil {
		return summary, err
	}
	summary.Deleted = true
	return summary, nil
}

func resolveFactorCredentialFile(configured string) string {
	for _, value := range []string{
		configured,
		os.Getenv("MOOX_FACTOR_EVENTBUS_ADMIN_CREDENTIAL_FILE"),
		os.Getenv("MOOX_STORAGE_EVENTBUS_ADMIN_CREDENTIAL_FILE"),
		os.Getenv("MOOX_EVENTBUS_INTERNAL_ADMIN_CREDENTIAL_FILE"),
		os.Getenv("MOOX_EVENTBUS_INTERNAL_CREDENTIAL_FILE"),
		"~/.config/moox/eventbus/internal-admin.yaml",
	} {
		if value = strings.TrimSpace(value); value != "" {
			return jetstream.ExpandCredentialPath(value)
		}
	}
	return ""
}

func resolveFactorPackageRoot(configured string) string {
	if root := strings.TrimSpace(configured); root != "" {
		return root
	}
	if root := strings.TrimSpace(os.Getenv("MOOX_FACTOR_PACKAGE_ROOT")); root != "" {
		return root
	}
	if executable, err := os.Executable(); err == nil {
		binDir := filepath.Dir(executable)
		candidates := []string{
			filepath.Dir(binDir),
			filepath.Dir(filepath.Dir(binDir)),
		}
		for _, candidate := range candidates {
			if info, statErr := os.Stat(filepath.Join(candidate, "restart.sh")); statErr == nil && !info.IsDir() {
				return candidate
			}
		}
		return candidates[0]
	}
	return ""
}

func runFactorLifecycleScript(ctx context.Context, packageRoot, action string) error {
	packageRoot = strings.TrimSpace(packageRoot)
	if packageRoot == "" {
		return errors.New("Factor package root is empty; pass --package-root")
	}
	script := filepath.Join(packageRoot, action+".sh")
	if info, err := os.Stat(script); err != nil || info.IsDir() {
		return fmt.Errorf("Factor lifecycle script is unavailable under %s", packageRoot)
	}
	cmd := exec.CommandContext(ctx, script, "factor")
	cmd.Dir = packageRoot
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}
