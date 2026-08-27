package deploy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	trpc "trpc.group/trpc-go/trpc-go"
)

type Options struct {
	RepositoryRoot string
	// DeployRoot is the shared parent on the target cloud disk. ControlRoot
	// and StorageRoot are the independently managed package directories below
	// it; empty values resolve to the canonical /data/moox layout.
	DeployRoot              string
	ControlRoot             string
	StorageRoot             string
	PublicHost              string
	NodeID                  string
	BrowserPort             int
	TargetGOOS              string
	TargetGOARCH            string
	ResetControlData        bool
	ResetStorageData        bool
	ResetViewData           bool
	UseControlGateway       bool
	EventBusPublicAddress   string
	EventBusPort            int
	EventBusTLSEnabled      bool
	NotificationChannelType string
	NotificationWebhookURL  string
	StoragePrimarySecret    string
	StorageViewSecret       string
	StorageViewPolicy       setupconfig.StorageView
	LocalLogs               setupconfig.LocalLogs
	HealthAuthVersion       string
	HealthAuthAccessKey     string
	HealthAuthSecretKey     string
	InstallStorageWatchdog  bool
	GatewayControlURL       string
	GatewayControlKey       string
	GatewayServiceKey       string
	GatewayCABundle         []byte
	TLSMode                 TLSMode
	// InstallLocalCA makes an internal-TLS control deployment verify that the
	// browser machine trusts the Caddy root certificate. This is intentionally
	// opt-in at the deployment package boundary so tests and non-browser
	// package consumers do not unexpectedly mutate the operator trust store.
	InstallLocalCA bool
}

type TLSMode string

const (
	TLSModePublic   TLSMode = "public"
	TLSModeInternal TLSMode = "internal"

	controlRollbackTimeout = 5 * time.Minute
	storageRollbackTimeout = 5 * time.Minute
)

func resolveTLSMode(mode TLSMode, publicHost string) TLSMode {
	if mode == TLSModePublic || mode == TLSModeInternal {
		return mode
	}
	host := strings.TrimSpace(publicHost)
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return TLSModeInternal
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()) {
		return TLSModeInternal
	}
	return TLSModePublic
}

// ResolveTLSMode exposes the same deterministic host-based selection used by
// the deployment and readiness paths to setup commands that need to choose the
// matching CA verification workflow.
func ResolveTLSMode(mode TLSMode, publicHost string) TLSMode {
	return resolveTLSMode(mode, publicHost)
}

func UsesPublicTLS(publicHost string) bool {
	return resolveTLSMode("", publicHost) == TLSModePublic
}

func UsesPublicTLSMode(mode TLSMode, publicHost string) bool {
	return resolveTLSMode(mode, publicHost) == TLSModePublic
}

// RequiresLocalCATrust reports whether browsers need the MooX Caddy root CA
// installed in the operator's trust store for the selected endpoint.
func RequiresLocalCATrust(mode TLSMode, publicHost string) bool {
	return resolveTLSMode(mode, publicHost) == TLSModeInternal
}

type Packager interface {
	Package(context.Context, Options) (string, error)
}

type ReadinessStage string

const (
	AdminReady          ReadinessStage = "admin_ready"
	SetupReady          ReadinessStage = "setup_ready"
	GatewayReady        ReadinessStage = "gateway_ready"
	EventBusReady       ReadinessStage = "eventbus_ready"
	CloudNodeReady      ReadinessStage = "cloudnode_ready"
	CollectorReady      ReadinessStage = "collector_ready"
	MonitorReady        ReadinessStage = "monitor_ready"
	WebReady            ReadinessStage = "web_ready"
	BrowserHTTPSReady   ReadinessStage = "browser_https_ready"
	StoragePrimaryReady ReadinessStage = "storage_primary_ready"
	StorageViewReady    ReadinessStage = "storage_view_ready"
)

type Probe interface {
	Wait(context.Context, setupssh.Client, ReadinessStage, Options) error
}

func normalizeDeployPaths(opts *Options) error {
	if opts == nil {
		return fmt.Errorf("paths_missing")
	}
	paths := (setupconfig.Paths{
		DeployRoot: opts.DeployRoot, ControlRoot: opts.ControlRoot, StorageRoot: opts.StorageRoot,
	}).Resolved()
	for _, value := range []string{paths.DeployRoot, paths.ControlRoot, paths.StorageRoot} {
		if value == "/" || !filepath.IsAbs(value) || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("paths_invalid")
		}
		for _, r := range value {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("/._-", r) {
				continue
			}
			return fmt.Errorf("paths_invalid")
		}
	}
	base := paths.DeployRoot + string(filepath.Separator)
	if paths.ControlRoot != paths.DeployRoot && !strings.HasPrefix(paths.ControlRoot, base) {
		return fmt.Errorf("paths_invalid")
	}
	if paths.StorageRoot != paths.DeployRoot && !strings.HasPrefix(paths.StorageRoot, base) {
		return fmt.Errorf("paths_invalid")
	}
	opts.DeployRoot, opts.ControlRoot, opts.StorageRoot = paths.DeployRoot, paths.ControlRoot, paths.StorageRoot
	if opts.LocalLogs.MaxSizeMB == 0 {
		opts.LocalLogs.MaxSizeMB = 50
	}
	if opts.LocalLogs.BackupCount == 0 {
		opts.LocalLogs.BackupCount = 5
	}
	return nil
}

// Storage deploys Access and the unified View runtime as one independently managed unit.
// It uses a separate install directory so selecting the control host cannot
// replace the Admin/Gateway/Web deployment.
func Storage(ctx context.Context, transport setupssh.Client, opts Options, deps Dependencies) (returnErr error) {
	if transport == nil || strings.TrimSpace(opts.RepositoryRoot) == "" || strings.TrimSpace(opts.PublicHost) == "" {
		return fmt.Errorf("storage_deploy_invalid")
	}
	if deps.Packager == nil {
		deps.Packager = StoragePackager{}
	}
	if deps.Probe == nil {
		deps.Probe = CommandProbe{}
	}
	if err := normalizeDeployPaths(&opts); err != nil {
		return fmt.Errorf("storage_deploy_invalid")
	}
	if err := detectPlatform(ctx, transport, &opts); err != nil {
		return fmt.Errorf("storage_platform_unsupported")
	}
	archive, err := deps.Packager.Package(ctx, opts)
	if err != nil {
		return fmt.Errorf("storage_package_failed")
	}
	defer os.Remove(archive)
	activationToken, err := newActivationToken()
	if err != nil {
		return fmt.Errorf("storage_package_failed")
	}
	remoteArchive := storageArchivePath(activationToken)
	file, err := os.Open(archive)
	if err != nil {
		return fmt.Errorf("storage_package_failed")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return fmt.Errorf("storage_package_failed")
	}
	if err := transport.Upload(ctx, file, info.Size(), remoteArchive, fs.FileMode(0o600)); err != nil {
		_ = file.Close()
		return fmt.Errorf("storage_upload_failed")
	}
	_ = file.Close()
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(trpc.BackgroundContext(), 10*time.Second)
		defer cancel()
		_, _ = transport.Run(cleanupCtx, []string{"rm", "-f", remoteArchive}, nil)
	}()
	reset := "0"
	if opts.ResetStorageData {
		reset = "1"
	}
	controlGateway := "0"
	if opts.UseControlGateway {
		controlGateway = "1"
	}
	viewReset := "0"
	if opts.ResetViewData {
		viewReset = "1"
	}
	if _, err := transport.Run(ctx, []string{
		"sh", "-lc", installStorageScript, "moox-install-storage", opts.StorageRoot, opts.ControlRoot, reset, viewReset, controlGateway, activationToken, remoteArchive,
	}, nil); err != nil {
		return fmt.Errorf("storage_install_failed")
	}
	installed := true
	defer func() {
		if returnErr != nil && installed {
			rollbackCtx, cancel := context.WithTimeout(trpc.BackgroundContext(), storageRollbackTimeout)
			defer cancel()
			if _, rollbackErr := transport.Run(rollbackCtx, []string{"sh", "-lc", rollbackStorageScript, "moox-rollback-storage", activationToken, opts.StorageRoot, opts.DeployRoot}, nil); rollbackErr != nil {
				returnErr = fmt.Errorf("%v; storage_rollback_failed", returnErr)
			}
		}
	}()
	for _, stage := range []ReadinessStage{StoragePrimaryReady, StorageViewReady} {
		if err := deps.Probe.Wait(ctx, transport, stage, opts); err != nil {
			return fmt.Errorf("storage_deploy_not_ready")
		}
	}
	if opts.InstallStorageWatchdog {
		if err := InstallStorageViewWatchdog(ctx, transport, opts.RepositoryRoot); err != nil {
			return fmt.Errorf("storage_watchdog_install_failed")
		}
	}
	installed = false
	_, _ = transport.Run(ctx, []string{"sh", "-lc", finalizeStorageScript, "moox-finalize-storage", activationToken, opts.StorageRoot, opts.DeployRoot}, nil)
	return nil
}

func storageArchivePath(activationToken string) string {
	return "/tmp/moox-storage-" + activationToken + ".tar.gz"
}

type Dependencies struct {
	Packager Packager
	Probe    Probe
	CAStore  CAStore
}

type CAStore interface{ Save(string, []byte) error }

func Control(ctx context.Context, transport setupssh.Client, opts Options, deps Dependencies) (returnErr error) {
	if transport == nil || strings.TrimSpace(opts.RepositoryRoot) == "" || strings.TrimSpace(opts.PublicHost) == "" {
		return fmt.Errorf("control_deploy_invalid")
	}
	if _, err := eventBusCommandEnv(nil, opts); err != nil {
		return err
	}
	opts.TLSMode = resolveTLSMode(opts.TLSMode, opts.PublicHost)
	if opts.BrowserPort == 0 {
		opts.BrowserPort = 9527
	}
	if opts.BrowserPort < 1 || opts.BrowserPort > 65535 {
		return fmt.Errorf("control_deploy_invalid")
	}
	if deps.Packager == nil {
		deps.Packager = CommandPackager{}
	}
	if deps.Probe == nil {
		deps.Probe = CommandProbe{}
	}
	if deps.CAStore == nil {
		deps.CAStore = FileCAStore{}
	}
	if err := normalizeDeployPaths(&opts); err != nil {
		return fmt.Errorf("control_deploy_invalid")
	}
	if err := detectPlatform(ctx, transport, &opts); err != nil {
		return fmt.Errorf("control_platform_unsupported")
	}
	activationToken, err := newActivationToken()
	if err != nil {
		return fmt.Errorf("control_deploy_invalid")
	}
	remoteArchive := controlArchivePath(activationToken)

	archive, err := deps.Packager.Package(ctx, opts)
	if err != nil {
		return fmt.Errorf("control_package_failed")
	}
	defer os.Remove(archive)
	file, err := os.Open(archive)
	if err != nil {
		return fmt.Errorf("control_package_failed")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return fmt.Errorf("control_package_failed")
	}
	if err := transport.Upload(ctx, file, info.Size(), remoteArchive, fs.FileMode(0o600)); err != nil {
		_ = file.Close()
		return fmt.Errorf("control_upload_failed")
	}
	_ = file.Close()
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(trpc.BackgroundContext(), 10*time.Second)
		defer cancel()
		_, _ = transport.Run(cleanupCtx, []string{"rm", "-f", remoteArchive}, nil)
	}()

	reset := "0"
	if opts.ResetControlData {
		reset = "1"
	}
	installResult, err := transport.Run(ctx, []string{
		"sh", "-lc", installControlScript, "moox-install-control",
		opts.ControlRoot, opts.PublicHost, strconv.Itoa(opts.BrowserPort), opts.TargetGOARCH, reset, string(opts.TLSMode), activationToken, remoteArchive,
	}, nil)
	if err != nil {
		return commandFailure("control_install_failed", installResult)
	}
	installed := true
	defer func() {
		if returnErr != nil && installed {
			rollbackCtx, cancel := context.WithTimeout(trpc.BackgroundContext(), controlRollbackTimeout)
			defer cancel()
			if _, rollbackErr := transport.Run(rollbackCtx, []string{"sh", "-lc", rollbackControlScript, "moox-rollback-control", activationToken, opts.ControlRoot}, nil); rollbackErr != nil {
				returnErr = fmt.Errorf("%v; control_rollback_failed", returnErr)
			}
		}
	}()
	for _, stage := range []ReadinessStage{
		AdminReady, SetupReady, GatewayReady, EventBusReady, CloudNodeReady,
		CollectorReady, MonitorReady, WebReady, BrowserHTTPSReady,
	} {
		if err := deps.Probe.Wait(ctx, transport, stage, opts); err != nil {
			fmt.Fprintf(os.Stderr, "control readiness failed stage=%s: %v\n", stage, err)
			return fmt.Errorf("control_deploy_not_ready")
		}
	}
	if opts.TLSMode == TLSModeInternal {
		ca, err := transport.Run(ctx, []string{"sh", "-lc", `cat "$1/certs/caddy/root.crt"`, "moox-read-control-ca", opts.ControlRoot}, nil)
		if err != nil || deps.CAStore.Save(opts.PublicHost, []byte(ca.Stdout)) != nil {
			return fmt.Errorf("control_ca_unavailable")
		}
		// Finalize the remote deployment before touching the operator trust
		// store. A missing sudo password must not roll back an otherwise healthy
		// control plane; the next CLI invocation can retry this idempotently.
		if opts.InstallLocalCA {
			installed = false
			_, _ = transport.Run(ctx, []string{"sh", "-lc", finalizeControlScript, "moox-finalize-control", activationToken, opts.ControlRoot, opts.DeployRoot}, nil)
			if err := EnsureLocalCATrustForHost(ctx, opts.RepositoryRoot, opts.PublicHost, opts.TLSMode); err != nil {
				return err
			}
			return nil
		}
	}
	// Once readiness and CA persistence succeed, the new deployment is authoritative.
	// A lost finalize response must never roll it back after previous was removed.
	installed = false
	_, _ = transport.Run(ctx, []string{"sh", "-lc", finalizeControlScript, "moox-finalize-control", activationToken, opts.ControlRoot, opts.DeployRoot}, nil)
	return nil
}

func commandFailure(code string, result setupssh.Result) error {
	detail := strings.TrimSpace(strings.Join([]string{result.Stderr, result.Stdout}, "\n"))
	detail = strings.Join(strings.Fields(detail), " ")
	if len(detail) > 500 {
		// Keep the start for context and the end where shell commands usually
		// report their actual failure after verbose tool output.
		detail = detail[:240] + " ... " + detail[len(detail)-256:]
	}
	if detail == "" {
		return fmt.Errorf("%s", code)
	}
	return fmt.Errorf("%s: %s", code, detail)
}

func newActivationToken() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func controlArchivePath(activationToken string) string {
	return "/tmp/moox-control-" + activationToken + ".tar.gz"
}

func validReleaseToken(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." || path.Base(value) != value {
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

func sha256File(file *os.File) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func parseSHA256(output string) (string, error) {
	fields := strings.Fields(output)
	if len(fields) == 0 || len(fields[0]) != sha256.Size*2 {
		return "", fmt.Errorf("invalid sha256 output")
	}
	if _, err := hex.DecodeString(fields[0]); err != nil {
		return "", fmt.Errorf("invalid sha256 output")
	}
	return strings.ToLower(fields[0]), nil
}

func resolveRemoteDeployDir(ctx context.Context, transport setupssh.Client, deployDir string) (string, error) {
	deployDir = strings.TrimSpace(deployDir)
	if deployDir == "" {
		deployDir = setupconfig.DefaultControlRoot
	}
	if strings.ContainsAny(deployDir, "\x00\r\n") {
		return "", fmt.Errorf("service_deploy_invalid")
	}
	if deployDir == "~" || strings.HasPrefix(deployDir, "~/") {
		home, err := transport.Run(ctx, []string{"sh", "-lc", `printf '%s' "$HOME"`}, nil)
		if err != nil {
			return "", fmt.Errorf("service_deploy_invalid")
		}
		remoteHome := strings.TrimSpace(home.Stdout)
		if remoteHome == "" || !strings.HasPrefix(remoteHome, "/") {
			return "", fmt.Errorf("service_deploy_invalid")
		}
		deployDir = path.Join(remoteHome, strings.TrimPrefix(deployDir, "~"))
	}
	if !strings.HasPrefix(deployDir, "/") || path.Clean(deployDir) == "/" {
		return "", fmt.Errorf("service_deploy_invalid")
	}
	return path.Clean(deployDir), nil
}

func detectPlatform(ctx context.Context, transport setupssh.Client, opts *Options) error {
	if opts.TargetGOOS == "" {
		result, err := transport.Run(ctx, []string{"uname", "-s"}, nil)
		if err != nil || strings.TrimSpace(strings.ToLower(result.Stdout)) != "linux" {
			return fmt.Errorf("unsupported")
		}
		opts.TargetGOOS = "linux"
	}
	if opts.TargetGOARCH == "" {
		result, err := transport.Run(ctx, []string{"uname", "-m"}, nil)
		if err != nil {
			return err
		}
		switch strings.TrimSpace(strings.ToLower(result.Stdout)) {
		case "x86_64", "amd64":
			opts.TargetGOARCH = "amd64"
		case "aarch64", "arm64":
			opts.TargetGOARCH = "arm64"
		default:
			return fmt.Errorf("unsupported")
		}
	}
	if opts.TargetGOOS != "linux" || (opts.TargetGOARCH != "amd64" && opts.TargetGOARCH != "arm64") {
		return fmt.Errorf("unsupported")
	}
	return nil
}

type CommandPackager struct{}

func (CommandPackager) Package(ctx context.Context, opts Options) (string, error) {
	if err := normalizeDeployPaths(&opts); err != nil {
		return "", err
	}
	root, err := filepath.Abs(opts.RepositoryRoot)
	if err != nil {
		return "", err
	}
	file, err := os.CreateTemp("", "moox-control-*.tar.gz")
	if err != nil {
		return "", err
	}
	archive := file.Name()
	if err := file.Close(); err != nil {
		return "", err
	}
	_ = os.Remove(archive)
	command := exec.CommandContext(ctx, filepath.Join(root, "scripts", "deploy-moox.sh"),
		"--profile", "control", "--package-only", "--archive", archive,
		"--target", "localhost", "--dir", opts.ControlRoot, "--goos", opts.TargetGOOS, "--goarch", opts.TargetGOARCH,
		// The control deployment is the complete non-trading application stack.
		// The profile intentionally defaults to a smaller control plane, so make
		// the requested Factor/Archive components explicit and keep Trade out.
		"--with-archive", "--with-factor", "--no-trade",
		"--public-host", opts.PublicHost, "--browser-https-port", strconv.Itoa(opts.BrowserPort),
		"--tls-mode", string(resolveTLSMode(opts.TLSMode, opts.PublicHost)),
		"--node-id", "control", "--gateway-control-url", "http://127.0.0.1:11000",
		"--monitor-instance-id", "monitor-control",
	)
	command.Dir = root
	command.Stdout = os.Stderr
	command.Stderr = os.Stderr
	command.Env, err = eventBusCommandEnv(os.Environ(), opts)
	if err != nil {
		_ = os.Remove(archive)
		return "", err
	}
	command.Env = notificationCommandEnv(command.Env, opts.NotificationChannelType, opts.NotificationWebhookURL)
	command.Env = localLogCommandEnv(command.Env, opts.LocalLogs)
	if err := command.Run(); err != nil {
		_ = os.Remove(archive)
		return "", err
	}
	return archive, nil
}

func eventBusCommandEnv(base []string, opts Options) ([]string, error) {
	address := strings.TrimSpace(opts.EventBusPublicAddress)
	if address == "" || opts.EventBusPort < 1 || opts.EventBusPort > 65535 || !opts.EventBusTLSEnabled {
		return nil, fmt.Errorf("control_deploy_invalid")
	}
	if strings.EqualFold(strings.TrimSuffix(address, "."), "localhost") {
		return nil, fmt.Errorf("control_deploy_invalid")
	}
	if ip := net.ParseIP(strings.Trim(address, "[]")); ip != nil && (ip.IsLoopback() || ip.IsUnspecified()) {
		return nil, fmt.Errorf("control_deploy_invalid")
	}
	const (
		tlsKey     = "MOOX_EVENTBUS_ENABLE_TLS"
		addressKey = "MOOX_EVENTBUS_PUBLIC_IP"
		portKey    = "MOOX_EVENTBUS_PORT"
	)
	env := make([]string, 0, len(base)+3)
	for _, entry := range base {
		key, _, found := strings.Cut(entry, "=")
		if found && (key == tlsKey || key == addressKey || key == portKey) {
			continue
		}
		env = append(env, entry)
	}
	return append(env,
		tlsKey+"=1",
		addressKey+"="+address,
		portKey+"="+strconv.Itoa(opts.EventBusPort),
	), nil
}

func notificationCommandEnv(base []string, channelType, webhook string) []string {
	const typeKey = "MOOX_NOTIFICATION_CHANNEL_TYPE"
	const urlKey = "MOOX_NOTIFICATION_WEBHOOK_URL"
	env := make([]string, 0, len(base)+2)
	for _, entry := range base {
		entryKey, _, found := strings.Cut(entry, "=")
		if found && (entryKey == typeKey || entryKey == urlKey) {
			continue
		}
		env = append(env, entry)
	}
	return append(env, typeKey+"="+channelType, urlKey+"="+webhook)
}

func localLogCommandEnv(base []string, policy setupconfig.LocalLogs) []string {
	return setCommandEnv(
		setCommandEnv(base, "MOOX_LOCAL_LOG_MAX_SIZE_MB", strconv.Itoa(policy.MaxSizeMB)),
		"MOOX_LOCAL_LOG_BACKUP_COUNT", strconv.Itoa(policy.BackupCount),
	)
}

type StoragePackager struct{}

func (StoragePackager) Package(ctx context.Context, opts Options) (string, error) {
	if err := normalizeDeployPaths(&opts); err != nil {
		return "", err
	}
	root, err := filepath.Abs(opts.RepositoryRoot)
	if err != nil {
		return "", err
	}
	skipBuild := os.Getenv("MOOX_SKIP_STORAGE_BUILD") == "1"
	if opts.TargetGOOS == "linux" && runtime.GOOS != "linux" && !skipBuild {
		executable, err := os.Executable()
		if err != nil {
			return "", err
		}
		command := exec.CommandContext(ctx, filepath.Join(root, "scripts", "build-storage-linux.sh"))
		command.Dir = root
		command.Env = append(os.Environ(),
			"MOOX_CLI="+executable,
			"CONFIG="+filepath.Join(root, "custom.toml"),
			// Storage uses CGO. Build on the selected deployment host so the
			// resulting binary cannot require a newer libc than that host has.
			"MOOX_STORAGE_BUILD_HOST="+storageNodeID(opts.NodeID),
		)
		if err := command.Run(); err != nil {
			return "", err
		}
		skipBuild = true
	}
	file, err := os.CreateTemp("", "moox-storage-*.tar.gz")
	if err != nil {
		return "", err
	}
	archive := file.Name()
	if err := file.Close(); err != nil {
		return "", err
	}
	_ = os.Remove(archive)
	controlURL := "http://127.0.0.1:11000"
	if !opts.UseControlGateway {
		controlURL = strings.TrimSpace(opts.GatewayControlURL)
		if controlURL == "" || strings.TrimSpace(opts.GatewayControlKey) == "" || strings.TrimSpace(opts.GatewayServiceKey) == "" || len(opts.GatewayCABundle) == 0 {
			return "", fmt.Errorf("remote storage package requires control gateway material")
		}
	}
	args := []string{
		"--profile", "storage", "--package-only", "--archive", archive,
		"--target", "localhost", "--dir", opts.StorageRoot, "--goos", opts.TargetGOOS, "--goarch", opts.TargetGOARCH,
		"--public-host", opts.PublicHost, "--node-id", storageNodeID(opts.NodeID), "--gateway-control-url", controlURL,
	}
	var gatewayFiles []string
	if !opts.UseControlGateway {
		for _, item := range []struct {
			prefix string
			value  []byte
		}{
			{"moox-gateway-control-", []byte(strings.TrimSpace(opts.GatewayControlKey))},
			{"moox-gateway-service-", []byte(strings.TrimSpace(opts.GatewayServiceKey))},
			{"moox-gateway-ca-", opts.GatewayCABundle},
		} {
			file, writeErr := os.CreateTemp("", item.prefix)
			if writeErr != nil {
				return "", writeErr
			}
			name := file.Name()
			if _, writeErr = file.Write(item.value); writeErr == nil {
				writeErr = file.Chmod(0o600)
			}
			if closeErr := file.Close(); writeErr == nil {
				writeErr = closeErr
			}
			if writeErr != nil {
				_ = os.Remove(name)
				return "", writeErr
			}
			gatewayFiles = append(gatewayFiles, name)
		}
		defer func() {
			for _, file := range gatewayFiles {
				_ = os.Remove(file)
			}
		}()
		args = append(args, "--gateway-control-key-file", gatewayFiles[0], "--gateway-service-key-file", gatewayFiles[1], "--gateway-ca-bundle", gatewayFiles[2])
	}
	if skipBuild {
		args = append(args, "--skip-build")
	}
	if opts.UseControlGateway {
		args = append(args, "--no-gateway")
	}
	command := exec.CommandContext(ctx, filepath.Join(root, "scripts", "deploy-moox.sh"), args...)
	command.Dir = root
	command.Stdout = os.Stderr
	command.Stderr = os.Stderr
	command.Env, err = eventBusCommandEnv(os.Environ(), opts)
	if err != nil {
		_ = os.Remove(archive)
		return "", err
	}
	command.Env = localLogCommandEnv(command.Env, opts.LocalLogs)
	if strings.TrimSpace(opts.StoragePrimarySecret) == "" || strings.TrimSpace(opts.StorageViewSecret) == "" {
		_ = os.Remove(archive)
		return "", fmt.Errorf("storage package requires control-owned internal auth secrets")
	}
	command.Env = append(command.Env,
		"MOOX_STORAGE_PRIMARY_AUTH_SECRET="+opts.StoragePrimarySecret,
		"MOOX_STORAGE_VIEW_AUTH_SECRET="+opts.StorageViewSecret,
	)
	policyPayload, err := json.Marshal(opts.StorageViewPolicy)
	if err != nil {
		_ = os.Remove(archive)
		return "", fmt.Errorf("encode storage view maintenance policy: %w", err)
	}
	// Keep standard padding: the deployment shell validates the payload with
	// Python's strict base64 decoder, which intentionally rejects raw encoding.
	command.Env = setCommandEnv(command.Env, "MOOX_STORAGE_VIEW_MAINTENANCE_POLICY_B64", base64.StdEncoding.EncodeToString(policyPayload))
	if strings.TrimSpace(opts.HealthAuthVersion) == "" ||
		strings.TrimSpace(opts.HealthAuthAccessKey) == "" ||
		strings.TrimSpace(opts.HealthAuthSecretKey) == "" {
		_ = os.Remove(archive)
		return "", fmt.Errorf("storage package requires control-owned health auth secrets")
	}
	command.Env = append(command.Env,
		"MOOX_HEALTH_AUTH_VERSION="+opts.HealthAuthVersion,
		"MOOX_HEALTH_AUTH_ACCESS_KEY="+opts.HealthAuthAccessKey,
		"MOOX_HEALTH_AUTH_SECRET_KEY="+opts.HealthAuthSecretKey,
	)
	if err := command.Run(); err != nil {
		_ = os.Remove(archive)
		return "", err
	}
	return archive, nil
}

func setCommandEnv(base []string, key, value string) []string {
	env := make([]string, 0, len(base)+1)
	for _, entry := range base {
		entryKey, _, found := strings.Cut(entry, "=")
		if found && entryKey == key {
			continue
		}
		env = append(env, entry)
	}
	return append(env, key+"="+value)
}

func storageNodeID(nodeID string) string {
	if nodeID = strings.TrimSpace(nodeID); nodeID != "" {
		return nodeID
	}
	return "storage"
}

type FileCAStore struct{}

func CAPath(publicHost string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	safe := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r) {
			return r
		}
		return '_'
	}, publicHost)
	return filepath.Join(home, ".moox", "certs", "moox-caddy-root-"+safe+".crt")
}

func (FileCAStore) Save(publicHost string, raw []byte) error {
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" || len(strings.TrimSpace(string(rest))) != 0 {
		return fmt.Errorf("invalid ca")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !certificate.IsCA {
		return fmt.Errorf("invalid ca")
	}
	path := CAPath(publicHost)
	if path == "" {
		return fmt.Errorf("invalid ca path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary := path + ".next"
	if err := os.WriteFile(temporary, raw, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

// EnsureLocalCATrust checks and, when needed, installs the public Caddy root
// certificate into the operator machine's trust store. The repository script
// owns the platform-specific trust-store details and prompts for elevation
// only when the current user cannot perform the operation directly.
func EnsureLocalCATrust(ctx context.Context, repositoryRoot, caPath string) error {
	repositoryRoot = strings.TrimSpace(repositoryRoot)
	caPath = strings.TrimSpace(caPath)
	if repositoryRoot == "" || caPath == "" {
		return fmt.Errorf("browser_ca_trust_failed: installer or CA path is empty")
	}
	script := filepath.Join(repositoryRoot, "scripts", "install-caddy-ca.sh")
	info, err := os.Stat(script)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("browser_ca_trust_failed: installer not found at %s", script)
	}
	if err := runLocalCACommand(ctx, script, caPath, true); err == nil {
		return nil
	}
	if err := runLocalCACommand(ctx, script, caPath, false); err != nil {
		return fmt.Errorf("browser_ca_trust_failed: install %s --ca-file %s: %w", script, caPath, err)
	}
	if err := runLocalCACommand(ctx, script, caPath, true); err != nil {
		return fmt.Errorf("browser_ca_trust_failed: trust store rejected %s after installation: %w", caPath, err)
	}
	return nil
}

// EnsureLocalCATrustForHost is the endpoint-aware form used by setup flows.
// Public ACME certificates are already trusted by normal browsers and must
// not cause any local trust-store mutation.
func EnsureLocalCATrustForHost(ctx context.Context, repositoryRoot, publicHost string, mode TLSMode) error {
	if !RequiresLocalCATrust(mode, publicHost) {
		return nil
	}
	return EnsureLocalCATrust(ctx, repositoryRoot, CAPath(publicHost))
}

func runLocalCACommand(ctx context.Context, script, caPath string, checkOnly bool) error {
	args := []string{"--ca-file", caPath}
	if checkOnly {
		args = append(args, "--check")
	}
	command := exec.CommandContext(ctx, script, args...)
	// Keep the command attached to the invoking terminal so sudo/security can
	// explain what is happening and request the user's administrator approval.
	command.Stdin = os.Stdin
	command.Stdout = os.Stderr
	command.Stderr = os.Stderr
	return command.Run()
}

type CommandProbe struct {
	Attempts int
	Delay    time.Duration
}

func (p CommandProbe) Wait(ctx context.Context, transport setupssh.Client, stage ReadinessStage, opts Options) error {
	attempts, delay := p.Attempts, p.Delay
	if attempts <= 0 {
		attempts = 30
	}
	// EventBus readiness includes the wildcard listener check. A freshly
	// restarted broker can report its health endpoint before the TLS listener
	// has completed binding; give this external dependency a bounded warm-up
	// window instead of rolling back an otherwise healthy deployment.
	if stage == EventBusReady && attempts < 300 {
		attempts = 300
	}
	if delay <= 0 {
		delay = time.Second
	}
	command := probeCommandForOptions(stage, opts)
	args := []string{"sh", "-lc", command, "moox-readiness", opts.PublicHost, strconv.Itoa(opts.BrowserPort), string(resolveTLSMode(opts.TLSMode, opts.PublicHost))}
	var lastResult setupssh.Result
	for attempt := 0; attempt < attempts; attempt++ {
		result, err := transport.Run(ctx, args, nil)
		lastResult = result
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	detail := strings.TrimSpace(strings.Join([]string{lastResult.Stderr, lastResult.Stdout}, " "))
	if len(detail) > 240 {
		detail = detail[len(detail)-240:]
	}
	if detail != "" {
		return fmt.Errorf("not_ready: %s", strings.Join(strings.Fields(detail), " "))
	}
	return fmt.Errorf("not_ready")
}

func probeCommand(stage ReadinessStage) string {
	return probeCommandForOptions(stage, Options{})
}

func probeCommandForOptions(stage ReadinessStage, opts Options) string {
	if err := normalizeDeployPaths(&opts); err != nil {
		return "false"
	}
	control := opts.ControlRoot
	storage := opts.StorageRoot
	switch stage {
	case AdminReady:
		return fmt.Sprintf(`%q/status.sh admin >/dev/null`, control)
	case SetupReady:
		return `curl -fsS -X POST -H 'Content-Type: application/json' -d '{}' http://127.0.0.1:11110/trpc.moox.admin.Setup/GetSetupStatus >/dev/null`
	case GatewayReady:
		return fmt.Sprintf(`%q/status.sh gateway >/dev/null`, control)
	case EventBusReady:
		// status.sh is human-oriented and historically returned success even
		// when a service was stopped. EventBus is an external dependency for
		// SCF, so setup readiness verifies the persisted listener contract and
		// the broker's local health listener instead.
		return fmt.Sprintf(`set -eu
root=%q
test -r "$root/config/runtime.env"
set -a; . "$root/config/runtime.env"; set +a
test "${MOOX_EVENTBUS_ENABLE_TLS:-}" = 1
# MOOX_EVENTBUS_HOST is the broker bind address, not the advertised SCF
# address. A public deployment must bind wildcard IPv4; 0.0.0.0 is expected.
test "${MOOX_EVENTBUS_HOST:-}" = 0.0.0.0
test -n "${MOOX_EVENTBUS_PORT:-}"
		# Probe the broker's local health listener. Any HTTP response is ready;
		# curl is intentionally used without -f so 401/404 still count.
		curl --connect-timeout 2 -sS -o /dev/null http://127.0.0.1:11419/`, control)
	case CloudNodeReady:
		return fmt.Sprintf(`%q/status.sh cloudnode >/dev/null`, control)
	case CollectorReady:
		return fmt.Sprintf(`%q/status.sh collector >/dev/null`, control)
	case MonitorReady:
		return fmt.Sprintf(`%q/status.sh monitor >/dev/null`, control)
	case WebReady:
		return fmt.Sprintf(`%q/status.sh web-host >/dev/null`, control)
	case BrowserHTTPSReady:
		return fmt.Sprintf(`if [ "$3" = internal ]; then curl -fsS --resolve "$1:$2:127.0.0.1" --cacert %q/certs/caddy/root.crt "https://$1:$2/" >/dev/null; else curl -fsS --resolve "$1:$2:127.0.0.1" "https://$1:$2/" >/dev/null; fi`, control)
	case StoragePrimaryReady:
		return fmt.Sprintf(`%q/status.sh storage-primary >/dev/null`, storage)
	case StorageViewReady:
		return fmt.Sprintf(`%q/status.sh storage-view >/dev/null`, storage)
	default:
		return "false"
	}
}

const installStorageScript = `set -eu
install_storage() {
  reset_storage_data="$1"
  reset_view_data="$2"
  use_control_gateway="$3"
  activation_token="$4"
  archive="$5"
  storage_root="${6:-${HOME}/moox/storage}"
  control_root="${7:-${HOME}/moox/prod}"
  case "$reset_storage_data" in 0|1) ;; *) echo storage_reset_invalid >&2; return 1 ;; esac
  case "$reset_view_data" in 0|1) ;; *) echo storage_view_reset_invalid >&2; return 1 ;; esac
  if [ "$reset_storage_data" = "1" ] && [ "$reset_view_data" = "1" ]; then echo storage_reset_flags_mutually_exclusive >&2; return 1; fi
  case "$use_control_gateway" in 0|1) ;; *) echo storage_gateway_invalid >&2; return 1 ;; esac
  case "$activation_token" in *[!A-Za-z0-9._-]*|'') echo storage_activation_token_invalid >&2; return 1 ;; esac
  [ "$archive" = "/tmp/moox-storage-$activation_token.tar.gz" ] || { echo storage_archive_invalid >&2; return 1; }
  deploy="$storage_root"
	root=$(dirname "$deploy")
	mkdir -p "$root"
	# Serialize package replacement with generated healthchecks. The lock lives
	# beside the directory so it survives atomic renames of the deployment.
	exec 8>"$deploy.maintenance.lock"
	flock -x 8
  next="$root/storage.next.$activation_token"
  previous="$root/storage.previous.$activation_token"
  failed="$root/storage.failed.$activation_token"
  install_healthcheck_cron() {
    if [ -n "${MOOX_CRON_DAEMON_CHECK_COMMAND:-}" ]; then
      "$MOOX_CRON_DAEMON_CHECK_COMMAND" || {
        echo 'storage_healthcheck_daemon_unavailable' >&2
        return 1
      }
    else
      command -v systemctl >/dev/null 2>&1 || {
        echo 'storage_healthcheck_daemon_unavailable' >&2
        return 1
      }
      cron_unit=""
      for unit in cron.service crond.service; do
        if systemctl is-active --quiet "$unit" && systemctl is-enabled --quiet "$unit"; then
          cron_unit="$unit"
          break
        fi
      done
      [ -n "$cron_unit" ] || {
        echo 'storage_healthcheck_daemon_unavailable' >&2
        return 1
      }
    fi
    crontab_command="${MOOX_CRONTAB_COMMAND:-crontab}"
    command -v "$crontab_command" >/dev/null 2>&1 || {
      echo 'storage_healthcheck_scheduler_unavailable' >&2
      return 1
    }
    if [ "$root" = "$HOME/moox" ]; then
      cron_line='* * * * * PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin; for healthcheck in "$HOME"/moox/*/healthcheck.sh; do root=$(dirname "$healthcheck"); case "$root" in *.next|*.next.*|*.previous|*.previous.*|*.failed|*.failed.*) continue ;; esac; if [ -x "$healthcheck" ]; then "$healthcheck" >/dev/null 2>&1 & fi; done; wait # moox-healthchecks'
    else
      cron_line='* * * * * PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin; for healthcheck in '"$root"'/*/healthcheck.sh; do root=$(dirname "$healthcheck"); case "$root" in *.next|*.next.*|*.previous|*.previous.*|*.failed|*.failed.*) continue ;; esac; if [ -x "$healthcheck" ]; then "$healthcheck" >/dev/null 2>&1 & fi; done; wait # moox-healthchecks'
    fi
    current=$("$crontab_command" -l 2>/dev/null || true)
    {
      printf '%s\n' "$current" | grep -Fv '# moox-healthchecks' || true
      printf '%s\n' "$cron_line"
    } | "$crontab_command" -
  }
  # Every independently installed MooX package participates in the same
  # host-level watchdog. Install the scheduler before replacing a working
  # Storage package so a missing cron daemon cannot leave it unsupervised.
  install_healthcheck_cron
  rm -rf "$next" "$previous" "$failed"
  mkdir -p "$next"
  tar -C "$next" -xzf "$archive"
  printf '%s\n' "$activation_token" >"$next/.storage-activation-token"
  date +%s >"$next/.storage-staged-at"
  preserve_data=0
  if [ "$reset_storage_data" = "0" ] && [ -d "$deploy/data" ]; then preserve_data=1; fi
  if [ -d "$deploy/secrets" ]; then
    for secret in "$deploy/secrets/"*; do
      [ -e "$secret" ] || continue
      case "$(basename "$secret")" in
        storage-internal-auth.env) continue ;;
        health-auth.env) [ -s "$next/secrets/health-auth.env" ] && continue ;;
      esac
      cp -R "$secret" "$next/secrets/"
    done
  fi
  mkdir -p "$next/secrets"
  if [ "$use_control_gateway" = "1" ]; then
    control_secrets="$control_root/secrets"
    # Control-owned internal auth is the single authority for both packages.
    # Do not preserve an older storage copy when the control deployment has
    # rotated the secret.
    for name in gateway-service.env gateway-storage-primary.key gateway-storage-view.key storage-internal-auth.env; do
      if [ ! -s "$control_secrets/$name" ]; then
        echo "storage_control_gateway_credentials_missing" >&2
        return 1
      fi
      cp "$control_secrets/$name" "$next/secrets/$name"
    done
  fi
  if [ ! -s "$next/secrets/health-auth.env" ]; then
    umask 077
    secret=$(head -c 32 /dev/urandom | base64 | tr -d '\n')
    printf 'MOOX_HEALTH_AUTH_VERSION=moox-health-v1\nMOOX_HEALTH_AUTH_ACCESS_KEY=monitor\nMOOX_HEALTH_AUTH_SECRET_KEY=%s\n' "$secret" >"$next/secrets/health-auth.env"
  fi
  chmod 600 "$next/secrets/health-auth.env"
  if [ -x "$deploy/stop.sh" ] && ! "$deploy/stop.sh"; then "$deploy/start.sh" 8>&- || true; return 1; fi
  # Storage data is normally the largest part of the package. Move it only
  # after the old processes stop instead of copying it into staging; copying
  # temporarily doubles disk use and can make an otherwise healthy upgrade
  # fail on a nearly-full volume.
  if [ "$preserve_data" = "1" ]; then
    rm -rf "$next/data"
    mv "$deploy/data" "$next/data"
  fi
  if [ -d "$deploy" ]; then mv "$deploy" "$previous"; date +%s >"$previous/.storage-staged-at"; fi
  mv "$next" "$deploy"
  rollback_failed_install() {
    "$deploy/stop.sh" || true
    restore_data="$root/storage.data.restore"
    rm -rf "$restore_data"
    if [ "$preserve_data" = "1" ] && [ -d "$deploy/data" ]; then mv "$deploy/data" "$restore_data"; fi
    mv "$deploy" "$failed"
    date +%s >"$failed/.storage-staged-at"
    if [ -d "$previous" ]; then
      mv "$previous" "$deploy"
      rm -f "$deploy/.storage-staged-at"
      if [ -d "$restore_data" ]; then rm -rf "$deploy/data"; mv "$restore_data" "$deploy/data"; fi
      "$deploy/start.sh" 8>&- || true
    fi
    return 1
  }
  if [ "$reset_view_data" = "1" ]; then
		# Reset needs JetStream management permissions (consumer delete and
		# subject purge); the storage consumer credential is intentionally scoped
		# to data delivery and cannot publish to $JS.API.STREAM.INFO.*.
    credential="$HOME/.config/moox/eventbus/internal-admin.yaml"
    [ -r "$credential" ] || { echo storage_view_reset_credential_missing >&2; rollback_failed_install; return 1; }
    if ! "$deploy/bin/moox-storage-cli" reset-view-consumers \
      --storage-conf "$deploy/storage/config/storage.yaml" \
      --package-root "$deploy" \
      --credential-file "$credential" \
      --restart=false --maintenance-lock-held --yes; then
      echo storage_view_reset_failed >&2
      rollback_failed_install
      return 1
    fi
  fi
  if ! "$deploy/start.sh" 8>&-; then
    rollback_failed_install
  fi
}
if [ "${1:-}" = 0 ] || [ "${1:-}" = 1 ]; then
  install_storage "$1" "$2" "$3" "$4" "$5"
else
  storage_root="$1"
  control_root="$2"
  shift 2
  install_storage "$1" "$2" "$3" "$4" "$5" "$storage_root" "$control_root"
fi
`

const rollbackStorageScript = `set -eu
# storage.previous rollback lineage is intentionally retained until finalize.
activation_token="$1"
deploy="${2:-${HOME}/moox/storage}"
root="${3:-${HOME}/moox}"
case "$activation_token" in *[!A-Za-z0-9._-]*|'') echo storage_activation_token_invalid >&2; exit 1 ;; esac
exec 8>"$deploy.maintenance.lock"
flock -x 8
previous="$root/storage.previous.$activation_token"
[ -s "$deploy/.storage-activation-token" ] || exit 0
[ "$(cat "$deploy/.storage-activation-token")" = "$activation_token" ] || exit 0
if [ ! -d "$previous" ]; then
  # A matching activation marker without a previous deployment is a failed
  # first installation. Stop and retain it for diagnosis instead of leaving a
  # deployment running after the CLI has reported failure.
  if [ -x "$deploy/stop.sh" ]; then "$deploy/stop.sh" || true; fi
  failed="$root/storage.failed.$activation_token"
  rm -rf "$failed"
  mv "$deploy" "$failed"
  date +%s >"$failed/.storage-staged-at"
  exit 0
fi
if [ -x "$deploy/stop.sh" ]; then "$deploy/stop.sh" || true; fi
restore_data="$root/storage.data.rollback.$activation_token"
rm -rf "$restore_data"
if [ -d "$deploy/data" ]; then mv "$deploy/data" "$restore_data"; fi
rm -rf "$deploy"
mv "$previous" "$deploy"
rm -f "$deploy/.storage-staged-at"
if [ -d "$restore_data" ]; then rm -rf "$deploy/data"; mv "$restore_data" "$deploy/data"; fi
"$deploy/start.sh" 8>&-
`

const finalizeStorageScript = `set -eu
# storage.maintenance.lock serializes finalization with the watchdog.
# storage.previous cleanup is scoped to the configured package root.
activation_token="$1"
deploy="${2:-${HOME}/moox/storage}"
root="${3:-${HOME}/moox}"
case "$activation_token" in *[!A-Za-z0-9._-]*|'') echo storage_activation_token_invalid >&2; exit 1 ;; esac
exec 8>"$deploy.maintenance.lock"
flock -x 8
[ -s "$deploy/.storage-activation-token" ] || exit 0
[ "$(cat "$deploy/.storage-activation-token")" = "$activation_token" ] || exit 0
deploy_base=$(basename "$deploy")
rm -rf "$root"/"$deploy_base".previous."$activation_token"
rm -f "$deploy/.storage-staged-at"
# Abandoned token directories are never authoritative. Keep recent ones for
# diagnosis and concurrent deployment fencing, but reclaim older leftovers so
# failed upgrades cannot slowly consume the Storage volume.
find "$root" -mindepth 2 -maxdepth 2 -type f -name '.storage-staged-at' -mtime +1 \
  -exec sh -c 'for marker do dir=${marker%/*}; case ${dir##*/} in storage.next.*|storage.failed.*|storage.previous.*) rm -rf -- "$dir" ;; esac; done' sh {} +
`

// The script is constant. Positional arguments contain only public deployment metadata.
const installControlScript = `set -eu
install_control() {
  public_host="$1"
  browser_port="$2"
  target_arch="$3"
  reset_data="$4"
  tls_mode="$5"
  activation_token="$6"
  archive="$7"
  deploy="${8:-${HOME}/moox/prod}"
  case "$reset_data" in 0|1) ;; *) echo 'control_reset_invalid' >&2; return 1 ;; esac
  case "$tls_mode" in public|internal) ;; *) echo 'control_tls_mode_invalid' >&2; return 1 ;; esac
  case "$activation_token" in *[!A-Za-z0-9._-]*|'') echo 'control_activation_token_invalid' >&2; return 1 ;; esac
  [ "$archive" = "/tmp/moox-control-$activation_token.tar.gz" ] || {
    echo 'control_archive_invalid' >&2
    return 1
  }
	root=$(dirname "$deploy")
	next="$deploy.next"
	previous="$deploy.previous.$activation_token"
	mkdir -p "$root"
  flock_command="${MOOX_FLOCK_COMMAND:-flock}"
  command -v "$flock_command" >/dev/null 2>&1 || { echo 'control_maintenance_lock_unavailable' >&2; return 1; }
  exec 8>"$deploy.maintenance.lock"
  "$flock_command" 8
  install_control_healthcheck_cron() {
    if [ -n "${MOOX_CRON_DAEMON_CHECK_COMMAND:-}" ]; then
      "$MOOX_CRON_DAEMON_CHECK_COMMAND" || {
        echo 'control_healthcheck_daemon_unavailable' >&2
        return 1
      }
    else
      command -v systemctl >/dev/null 2>&1 || {
        echo 'control_healthcheck_daemon_unavailable' >&2
        return 1
      }
      cron_unit=""
      for unit in cron.service crond.service; do
        if systemctl is-active --quiet "$unit" && systemctl is-enabled --quiet "$unit"; then
          cron_unit="$unit"
          break
        fi
      done
      [ -n "$cron_unit" ] || {
        echo 'control_healthcheck_daemon_unavailable' >&2
        return 1
      }
    fi
    crontab_command="${MOOX_CRONTAB_COMMAND:-crontab}"
    command -v "$crontab_command" >/dev/null 2>&1 || {
      echo 'control_healthcheck_scheduler_unavailable' >&2
      return 1
    }
    if [ "$root" = "$HOME/moox" ]; then
      cron_line='* * * * * PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin; for healthcheck in "$HOME"/moox/*/healthcheck.sh; do root=$(dirname "$healthcheck"); case "$root" in *.next|*.next.*|*.previous|*.previous.*|*.failed|*.failed.*) continue ;; esac; if [ -x "$healthcheck" ]; then "$healthcheck" >/dev/null 2>&1 & fi; done; wait # moox-healthchecks'
    else
      cron_line='* * * * * PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin; for healthcheck in '"$root"'/*/healthcheck.sh; do root=$(dirname "$healthcheck"); case "$root" in *.next|*.next.*|*.previous|*.previous.*|*.failed|*.failed.*) continue ;; esac; if [ -x "$healthcheck" ]; then "$healthcheck" >/dev/null 2>&1 & fi; done; wait # moox-healthchecks'
    fi
    current=$("$crontab_command" -l 2>/dev/null || true)
    {
      printf '%s\n' "$current" | grep -Fv '# moox-healthchecks' || true
      printf '%s\n' "$cron_line"
    } | "$crontab_command" -
  }
  # Install the scheduler before any deployment mutation. If cron is
  # unavailable, the existing deployment remains untouched.
  install_control_healthcheck_cron
  rm -rf "$next" "$previous"
  mkdir -p "$next"
  tar -C "$next" -xzf "$archive"
  # Storage can be installed independently after the control package.  Keep
  # that explicit topology decision across a later control upgrade; the
  # freshly packaged default is zero because the control package cannot know
  # which Storage roots exist on the target host.
  old_components="$deploy/config/components.env"
  next_components="$next/config/components.env"
  rotate_eventbus=0
  if [ "$reset_data" = 1 ] && [ -r "$deploy/start.sh" ] && [ -r "$next/start.sh" ]; then
    old_eventbus_url=$(sed -n 's/^EVENTBUS_URL_ENV="${MOOX_EVENTBUS_NATS_URL:-\(.*\)}"$/\1/p' "$deploy/start.sh" | head -n 1)
    next_eventbus_url=$(sed -n 's/^EVENTBUS_URL_ENV="${MOOX_EVENTBUS_NATS_URL:-\(.*\)}"$/\1/p' "$next/start.sh" | head -n 1)
    if [ -n "$old_eventbus_url" ] && [ -n "$next_eventbus_url" ] && [ "$old_eventbus_url" != "$next_eventbus_url" ]; then
      rotate_eventbus=1
    fi
  fi
  if [ -r "$old_components" ] && [ -r "$next_components" ] &&
    grep -q '^MOOX_PRESERVE_STORAGE_ROUTES=1$' "$old_components" &&
    ! grep -q '^MOOX_PRESERVE_STORAGE_ROUTES=1$' "$next_components"; then
    policy_tmp="$next_components.tmp.$$"
    trap 'rm -f "$policy_tmp"' EXIT
    awk '!/^MOOX_PRESERVE_STORAGE_ROUTES=/' "$next_components" >"$policy_tmp"
    printf '%s\n' 'MOOX_PRESERVE_STORAGE_ROUTES=1' >>"$policy_tmp"
    chmod --reference="$next_components" "$policy_tmp" 2>/dev/null || chmod 0600 "$policy_tmp"
    mv -f "$policy_tmp" "$next_components"
    trap - EXIT
  fi
  # A reset intentionally drops admin.db, but EventBus role files are shared
  # with Storage and other peers. Persist the decision in the deployment
  # package so restart.sh and the healthcheck keep those external identities
  # instead of trying to export missing secrets from the fresh database.
  if [ -r "$next_components" ]; then
    preserve_external_eventbus=0
    if [ "$rotate_eventbus" != 1 ] && { [ "$reset_data" = 1 ] || {
      [ -r "$old_components" ] && grep -q '^MOOX_PRESERVE_EXTERNAL_EVENTBUS_CREDENTIALS=1$' "$old_components"
    }; }; then
      preserve_external_eventbus=1
    fi
    eventbus_policy_tmp="$next_components.eventbus.$$"
    trap 'rm -f "$eventbus_policy_tmp"' EXIT
    awk '!/^MOOX_PRESERVE_EXTERNAL_EVENTBUS_CREDENTIALS=/' "$next_components" >"$eventbus_policy_tmp"
    if [ "$preserve_external_eventbus" = 1 ]; then
      printf '%s\n' 'MOOX_PRESERVE_EXTERNAL_EVENTBUS_CREDENTIALS=1' >>"$eventbus_policy_tmp"
    fi
    chmod --reference="$next_components" "$eventbus_policy_tmp" 2>/dev/null || chmod 0600 "$eventbus_policy_tmp"
    mv -f "$eventbus_policy_tmp" "$next_components"
    trap - EXIT
  fi
  printf '%s\n' "$activation_token" >"$next/.control-activation-token"
  caddy_stopped=0
  restart_stopped_caddy() {
    [ "$caddy_stopped" = 1 ] || return 0
    candidate="$deploy"
    [ -x "$candidate/lib/caddy-managed.sh" ] || candidate="$previous"
    if [ -x "$candidate/lib/caddy-managed.sh" ]; then
      "$candidate/lib/caddy-managed.sh" start --deploy-dir "$candidate" --os linux --arch "$target_arch" 8>&- || true
    fi
  }
  on_install_control_exit() {
    status=$?
    trap - EXIT
    if [ "$status" -ne 0 ]; then restart_stopped_caddy; fi
    exit "$status"
  }
  trap on_install_control_exit EXIT
  # Caddy owns files in its ACME storage while running. Stop it before copying
  # that directory into the atomic replacement.
  if [ -x "$deploy/lib/caddy-managed.sh" ]; then
    "$deploy/lib/caddy-managed.sh" stop --deploy-dir "$deploy" --os linux --arch "$target_arch"
    caddy_stopped=1
  fi
  if [ "$reset_data" = 0 ] && [ -d "$deploy/data" ]; then cp -R "$deploy/data/." "$next/data/"; fi
  if [ "$reset_data" = 1 ] && [ -d "$deploy/data/caddy" ]; then
    mkdir -p "$next/data/caddy"
    cp -R "$deploy/data/caddy/." "$next/data/caddy/"
  fi
  packaged_storage_auth="$next/secrets/storage-internal-auth.env.packaged"
  if [ -s "$next/secrets/storage-internal-auth.env" ]; then
    mv "$next/secrets/storage-internal-auth.env" "$packaged_storage_auth"
  fi
  if [ -d "$deploy/secrets" ]; then cp -R "$deploy/secrets/." "$next/secrets/"; fi
  mkdir -p "$HOME/.config/moox/credentials" "$next/secrets"
  if [ -s "$packaged_storage_auth" ]; then
    if [ ! -s "$next/secrets/storage-internal-auth.env" ]; then
      mv "$packaged_storage_auth" "$next/secrets/storage-internal-auth.env"
    else
      for key in MOOX_STORAGE_PRIMARY_AUTH_SECRET MOOX_STORAGE_VIEW_AUTH_SECRET; do
        if ! grep -q "^${key}=" "$next/secrets/storage-internal-auth.env"; then
          line=$(grep -m1 "^${key}=" "$packaged_storage_auth" || true)
          if [ -z "$line" ]; then
            echo 'storage_internal_auth_invalid' >&2
            return 1
          fi
          if [ -n "$(tail -c 1 "$next/secrets/storage-internal-auth.env")" ]; then
            printf '\n' >>"$next/secrets/storage-internal-auth.env"
          fi
          printf '%s\n' "$line" >>"$next/secrets/storage-internal-auth.env"
        fi
      done
      rm -f "$packaged_storage_auth"
    fi
  fi
  if [ -f "$next/secrets/notification.env.next" ]; then
    mv -f "$next/secrets/notification.env.next" "$next/secrets/notification.env"
  fi
  chmod 700 "$HOME/.config/moox" "$HOME/.config/moox/credentials"
  encryption_key="$HOME/.config/moox/credentials/admin-encryption-key"
  if [ ! -s "$encryption_key" ]; then
    if [ -s "$next/data/admin.db" ]; then
      echo 'admin_encryption_key_missing' >&2
      return 1
    fi
    umask 077
    head -c 32 /dev/urandom | base64 | tr -d '\n' >"$encryption_key"
  fi
  chmod 600 "$encryption_key"
  if [ ! -s "$next/secrets/health-auth.env" ]; then
    secret=$("$next/bin/moox-admin-cli" random-secret --bytes 32 | sed -n 's/.*"secret":"\([^"]*\)".*/\1/p')
    umask 077
    printf 'MOOX_HEALTH_AUTH_VERSION=moox-health-v1\nMOOX_HEALTH_AUTH_ACCESS_KEY=monitor\nMOOX_HEALTH_AUTH_SECRET_KEY=%s\n' "$secret" >"$next/secrets/health-auth.env"
  fi
  if [ ! -s "$next/secrets/admin-jwt.env" ]; then
    secret=$("$next/bin/moox-admin-cli" random-secret --bytes 32 | sed -n 's/.*"secret":"\([^"]*\)".*/\1/p')
    umask 077
    printf 'MOOX_ADMIN_JWT_SECRET_KEY=%s\n' "$secret" >"$next/secrets/admin-jwt.env"
  fi
  chmod 600 "$next/secrets/"*
  if [ -x "$deploy/stop.sh" ] && ! "$deploy/stop.sh"; then
    "$deploy/start.sh" 8>&- || true
    restart_stopped_caddy
    caddy_stopped=0
    return 1
  fi
  eventbus_backup=""
  if [ "$rotate_eventbus" = 1 ] && [ -d "$HOME/.config/moox/eventbus" ]; then
    eventbus_backup="$HOME/.config/moox/eventbus.previous.$activation_token"
    rm -rf "$eventbus_backup"
    cp -R "$HOME/.config/moox/eventbus" "$eventbus_backup"
  fi
  restore_eventbus_backup() {
    [ -n "$eventbus_backup" ] || return 0
    rm -rf "$HOME/.config/moox/eventbus"
    mv "$eventbus_backup" "$HOME/.config/moox/eventbus"
    eventbus_backup=""
  }
  discard_eventbus_backup() {
    [ -n "$eventbus_backup" ] || return 0
    rm -rf "$eventbus_backup"
    eventbus_backup=""
  }
  if [ -d "$deploy" ]; then mv "$deploy" "$previous"; fi
  mv "$next" "$deploy"
  caddy_ports="$browser_port,11001"
  if ! MOOX_PUBLIC_HOST="$public_host" MOOX_BROWSER_HTTPS_PORT="$browser_port" MOOX_SERVICE_HTTPS_PORT=11001 \
    MOOX_TLS_MODE="$tls_mode" \
    MOOX_CADDY_CHECKSUMS="$deploy/lib/caddy-v2.11.4-checksums.txt" \
    MOOX_CADDY_ARCHIVE="$deploy/lib/caddy_2.11.4_linux_${target_arch}.tar.gz" \
    "$deploy/lib/caddy-managed.sh" ensure --deploy-dir "$deploy" --os linux --arch "$target_arch" \
      --ports "$caddy_ports" --config "$deploy/config/caddy/Caddyfile.next" 8>&-; then
    if [ -x "$deploy/lib/caddy-managed.sh" ] && ! "$deploy/lib/caddy-managed.sh" stop --deploy-dir "$deploy" --os linux --arch "$target_arch"; then
      echo 'managed Caddy could not be stopped; leaving the failed deployment in place for safe retry' >&2
      return 1
    fi
    rm -rf "$deploy"
    restore_eventbus_backup
    if [ -d "$previous" ]; then
      mv "$previous" "$deploy"
      "$deploy/start.sh" 8>&- || true
      [ ! -x "$deploy/lib/caddy-managed.sh" ] || "$deploy/lib/caddy-managed.sh" start --deploy-dir "$deploy" --os linux --arch "$target_arch" 8>&- || true
    fi
    caddy_stopped=0
    return 1
  fi
  if ! MOOX_RESET_CONTROL_DATA="$reset_data" MOOX_EVENTBUS_ROTATE_CREDENTIALS="$rotate_eventbus" "$deploy/start.sh" 8>&-; then
    if [ -x "$deploy/lib/caddy-managed.sh" ] && ! "$deploy/lib/caddy-managed.sh" stop --deploy-dir "$deploy" --os linux --arch "$target_arch"; then
      echo 'managed Caddy could not be stopped; leaving the failed deployment in place for safe retry' >&2
      return 1
    fi
    "$deploy/stop.sh" || true
    rm -rf "$deploy"
    restore_eventbus_backup
    if [ -d "$previous" ]; then
      mv "$previous" "$deploy"
      "$deploy/start.sh" 8>&- || true
      [ ! -x "$deploy/lib/caddy-managed.sh" ] || "$deploy/lib/caddy-managed.sh" start --deploy-dir "$deploy" --os linux --arch "$target_arch" 8>&- || true
    fi
    caddy_stopped=0
    return 1
  fi
  # Keep the old credentials until the outer readiness probe succeeds. The
  # finalize/rollback scripts own cleanup so a post-install probe failure can
  # restore the previous deployment atomically.
  eventbus_backup=""
  caddy_stopped=0
}
case "${1:-}" in
/*)
  deploy_root="$1"
  shift
  install_control "$1" "$2" "$3" "$4" "$5" "$6" "$7" "$deploy_root"
  ;;
*)
  install_control "$1" "$2" "$3" "$4" "$5" "$6" "$7"
  ;;
esac`

const rollbackControlScript = `set -eu
# prod.previous rollback lineage is intentionally retained until finalize.
activation_token="$1"
deploy="${2:-${HOME}/moox/prod}"
root="${3:-$(dirname "$deploy")}"
case "$activation_token" in *[!A-Za-z0-9._-]*|'') echo 'control_activation_token_invalid' >&2; exit 1 ;; esac
previous="$deploy.previous.$activation_token"
eventbus_backup="$HOME/.config/moox/eventbus.previous.$activation_token"
flock_command="${MOOX_FLOCK_COMMAND:-flock}"
command -v "$flock_command" >/dev/null 2>&1 || { echo 'control_maintenance_lock_unavailable' >&2; exit 1; }
exec 8>"$deploy.maintenance.lock"
"$flock_command" 8
[ -s "$deploy/.control-activation-token" ] || exit 0
[ "$(cat "$deploy/.control-activation-token")" = "$activation_token" ] || exit 0
if [ -x "$deploy/lib/caddy-managed.sh" ] && ! "$deploy/lib/caddy-managed.sh" stop --deploy-dir "$deploy" --os linux --arch "$(uname -m)"; then
  echo 'managed Caddy could not be stopped; refusing destructive rollback' >&2
  exit 1
fi
if [ -x "$deploy/stop.sh" ]; then "$deploy/stop.sh" || true; fi
rm -rf "$deploy"
if [ -d "$eventbus_backup" ]; then
  rm -rf "$HOME/.config/moox/eventbus"
  mv "$eventbus_backup" "$HOME/.config/moox/eventbus"
fi
if [ -d "$previous" ]; then
  mv "$previous" "$deploy"
  "$deploy/start.sh" 8>&-
  [ ! -x "$deploy/lib/caddy-managed.sh" ] || "$deploy/lib/caddy-managed.sh" start --deploy-dir "$deploy" --os linux --arch "$(uname -m)" 8>&-
fi`

const finalizeControlScript = `set -eu
# prod.previous cleanup is scoped to the configured package root.
activation_token="$1"
deploy="${2:-${HOME}/moox/prod}"
root="${3:-$(dirname "$deploy")}"
eventbus_backup="$HOME/.config/moox/eventbus.previous.$activation_token"
case "$activation_token" in *[!A-Za-z0-9._-]*|'') echo 'control_activation_token_invalid' >&2; exit 1 ;; esac
flock_command="${MOOX_FLOCK_COMMAND:-flock}"
command -v "$flock_command" >/dev/null 2>&1 || { echo 'control_maintenance_lock_unavailable' >&2; exit 1; }
exec 8>"$deploy.maintenance.lock"
"$flock_command" 8
[ -s "$deploy/.control-activation-token" ] || exit 0
[ "$(cat "$deploy/.control-activation-token")" = "$activation_token" ] || exit 0
deploy_base=$(basename "$deploy")
rm -rf "$root"/"$deploy_base".previous.*
rm -rf "$eventbus_backup"
rm -f "$deploy/.control-activation-token"`
