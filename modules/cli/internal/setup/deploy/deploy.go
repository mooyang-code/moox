package deploy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
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

	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	trpc "trpc.group/trpc-go/trpc-go"
)

const (
	remoteStorageArchiveNext = "/tmp/moox-storage.tar.gz.next"
)

type Options struct {
	RepositoryRoot         string
	PublicHost             string
	NodeID                 string
	BrowserPort            int
	TargetGOOS             string
	TargetGOARCH           string
	ResetControlData       bool
	ResetStorageData       bool
	UseControlGateway      bool
	EventBusPublicAddress  string
	EventBusPort           int
	EventBusTLSEnabled     bool
	MonitoringWeComWebhook string
	StoragePrimarySecret   string
	StorageViewSecret      string
	HealthAuthVersion      string
	HealthAuthAccessKey    string
	HealthAuthSecretKey    string
	InstallStorageWatchdog bool
	GatewayControlURL      string
	GatewayControlKey      string
	GatewayServiceKey      string
	GatewayCABundle        []byte
	TLSMode                TLSMode
}

type TLSMode string

const (
	TLSModePublic   TLSMode = "public"
	TLSModeInternal TLSMode = "internal"

	controlRollbackTimeout = 5 * time.Minute
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

func UsesPublicTLS(publicHost string) bool {
	return resolveTLSMode("", publicHost) == TLSModePublic
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
	if err := detectPlatform(ctx, transport, &opts); err != nil {
		return fmt.Errorf("storage_platform_unsupported")
	}
	archive, err := deps.Packager.Package(ctx, opts)
	if err != nil {
		return fmt.Errorf("storage_package_failed")
	}
	defer os.Remove(archive)
	file, err := os.Open(archive)
	if err != nil {
		return fmt.Errorf("storage_package_failed")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return fmt.Errorf("storage_package_failed")
	}
	if err := transport.Upload(ctx, file, info.Size(), remoteStorageArchiveNext, fs.FileMode(0o600)); err != nil {
		_ = file.Close()
		return fmt.Errorf("storage_upload_failed")
	}
	_ = file.Close()
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(trpc.BackgroundContext(), 10*time.Second)
		defer cancel()
		_, _ = transport.Run(cleanupCtx, []string{"rm", "-f", remoteStorageArchiveNext}, nil)
	}()
	reset := "0"
	if opts.ResetStorageData {
		reset = "1"
	}
	controlGateway := "0"
	if opts.UseControlGateway {
		controlGateway = "1"
	}
	if _, err := transport.Run(ctx, []string{
		"sh", "-lc", installStorageScript, "moox-install-storage", reset, controlGateway,
	}, nil); err != nil {
		return fmt.Errorf("storage_install_failed")
	}
	installed := true
	defer func() {
		if returnErr != nil && installed {
			rollbackCtx, cancel := context.WithTimeout(trpc.BackgroundContext(), 30*time.Second)
			defer cancel()
			_, _ = transport.Run(rollbackCtx, []string{"sh", "-lc", rollbackStorageScript}, nil)
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
	_, _ = transport.Run(ctx, []string{"sh", "-lc", finalizeStorageScript}, nil)
	return nil
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
		opts.PublicHost, strconv.Itoa(opts.BrowserPort), opts.TargetGOARCH, reset, string(opts.TLSMode), activationToken, remoteArchive,
	}, nil)
	if err != nil {
		return commandFailure("control_install_failed", installResult)
	}
	installed := true
	defer func() {
		if returnErr != nil && installed {
			rollbackCtx, cancel := context.WithTimeout(trpc.BackgroundContext(), controlRollbackTimeout)
			defer cancel()
			if _, rollbackErr := transport.Run(rollbackCtx, []string{"sh", "-lc", rollbackControlScript, "moox-rollback-control", activationToken}, nil); rollbackErr != nil {
				returnErr = fmt.Errorf("%v; control_rollback_failed", returnErr)
			}
		}
	}()
	for _, stage := range []ReadinessStage{
		AdminReady, SetupReady, GatewayReady, EventBusReady, CloudNodeReady,
		CollectorReady, MonitorReady, WebReady, BrowserHTTPSReady,
	} {
		if err := deps.Probe.Wait(ctx, transport, stage, opts); err != nil {
			return fmt.Errorf("control_deploy_not_ready")
		}
	}
	if opts.TLSMode == TLSModeInternal {
		ca, err := transport.Run(ctx, []string{"sh", "-lc", `cat "$HOME/moox/prod/certs/caddy/root.crt"`}, nil)
		if err != nil || deps.CAStore.Save(opts.PublicHost, []byte(ca.Stdout)) != nil {
			return fmt.Errorf("control_ca_unavailable")
		}
	}
	// Once readiness and CA persistence succeed, the new deployment is authoritative.
	// A lost finalize response must never roll it back after previous was removed.
	installed = false
	_, _ = transport.Run(ctx, []string{"sh", "-lc", finalizeControlScript, "moox-finalize-control", activationToken}, nil)
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
		deployDir = "~/moox/prod"
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
		"--target", "localhost", "--dir", "~/moox/prod", "--goos", opts.TargetGOOS, "--goarch", opts.TargetGOARCH,
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
	command.Env = monitoringCommandEnv(command.Env, opts.MonitoringWeComWebhook)
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

func monitoringCommandEnv(base []string, webhook string) []string {
	const key = "MOOX_MSGBOX_WECOM_WEBHOOK"
	env := make([]string, 0, len(base)+1)
	for _, entry := range base {
		entryKey, _, found := strings.Cut(entry, "=")
		if found && entryKey == key {
			continue
		}
		env = append(env, entry)
	}
	return append(env, key+"="+webhook)
}

type StoragePackager struct{}

func (StoragePackager) Package(ctx context.Context, opts Options) (string, error) {
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
		"--target", "localhost", "--dir", "~/moox/storage", "--goos", opts.TargetGOOS, "--goarch", opts.TargetGOARCH,
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
	if strings.TrimSpace(opts.StoragePrimarySecret) == "" || strings.TrimSpace(opts.StorageViewSecret) == "" {
		_ = os.Remove(archive)
		return "", fmt.Errorf("storage package requires control-owned internal auth secrets")
	}
	command.Env = append(command.Env,
		"MOOX_STORAGE_PRIMARY_AUTH_SECRET="+opts.StoragePrimarySecret,
		"MOOX_STORAGE_VIEW_AUTH_SECRET="+opts.StorageViewSecret,
	)
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

type CommandProbe struct {
	Attempts int
	Delay    time.Duration
}

func (p CommandProbe) Wait(ctx context.Context, transport setupssh.Client, stage ReadinessStage, opts Options) error {
	attempts, delay := p.Attempts, p.Delay
	if attempts <= 0 {
		attempts = 30
	}
	if delay <= 0 {
		delay = time.Second
	}
	command := probeCommand(stage)
	args := []string{"sh", "-lc", command, "moox-readiness", opts.PublicHost, strconv.Itoa(opts.BrowserPort), string(resolveTLSMode(opts.TLSMode, opts.PublicHost))}
	for attempt := 0; attempt < attempts; attempt++ {
		if _, err := transport.Run(ctx, args, nil); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	return fmt.Errorf("not_ready")
}

func probeCommand(stage ReadinessStage) string {
	switch stage {
	case AdminReady:
		return `"$HOME/moox/prod/status.sh" admin >/dev/null`
	case SetupReady:
		return `curl -fsS -X POST -H 'Content-Type: application/json' -d '{}' http://127.0.0.1:11110/trpc.moox.admin.Setup/GetSetupStatus >/dev/null`
	case GatewayReady:
		return `"$HOME/moox/prod/status.sh" gateway >/dev/null`
	case EventBusReady:
		return `"$HOME/moox/prod/status.sh" eventbus >/dev/null`
	case CloudNodeReady:
		return `"$HOME/moox/prod/status.sh" cloudnode >/dev/null`
	case CollectorReady:
		return `"$HOME/moox/prod/status.sh" collector >/dev/null`
	case MonitorReady:
		return `"$HOME/moox/prod/status.sh" monitor >/dev/null`
	case WebReady:
		return `"$HOME/moox/prod/status.sh" web-host >/dev/null`
	case BrowserHTTPSReady:
		return `if [ "$3" = internal ]; then curl -fsS --resolve "$1:$2:127.0.0.1" --cacert "$HOME/moox/prod/certs/caddy/root.crt" "https://$1:$2/" >/dev/null; else curl -fsS --resolve "$1:$2:127.0.0.1" "https://$1:$2/" >/dev/null; fi`
	case StoragePrimaryReady:
		return `"$HOME/moox/storage/status.sh" storage-primary >/dev/null`
	case StorageViewReady:
		return `"$HOME/moox/storage/status.sh" storage-view >/dev/null`
	default:
		return "false"
	}
}

const installStorageScript = `set -eu
install_storage() {
  reset_storage_data="$1"
  use_control_gateway="$2"
  case "$reset_storage_data" in 0|1) ;; *) echo storage_reset_invalid >&2; return 1 ;; esac
  case "$use_control_gateway" in 0|1) ;; *) echo storage_gateway_invalid >&2; return 1 ;; esac
  root="$HOME/moox"
  deploy="$root/storage"
  next="$root/storage.next"
  previous="$root/storage.previous"
  failed="$root/storage.failed"
  archive=/tmp/moox-storage.tar.gz.next
  rm -rf "$next" "$previous" "$failed"
  mkdir -p "$next"
  tar -C "$next" -xzf "$archive"
  if [ "$reset_storage_data" = "0" ] && [ -d "$deploy/data" ]; then cp -R "$deploy/data/." "$next/data/"; fi
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
    control_secrets="$root/prod/secrets"
    for name in gateway-service.env gateway-storage-primary.key gateway-storage-view.key; do
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
  if [ -x "$deploy/stop.sh" ] && ! "$deploy/stop.sh"; then "$deploy/start.sh" || true; return 1; fi
  if [ -d "$deploy" ]; then mv "$deploy" "$previous"; fi
  mv "$next" "$deploy"
  if ! "$deploy/start.sh"; then
    "$deploy/stop.sh" || true
    mv "$deploy" "$failed"
    if [ -d "$previous" ]; then mv "$previous" "$deploy"; "$deploy/start.sh" || true; fi
    return 1
  fi
}
install_storage "$1" "$2"
`

const rollbackStorageScript = `set -eu
deploy="$HOME/moox/storage"
previous="$HOME/moox/storage.previous"
if [ ! -d "$previous" ]; then
  # The installer already restored the previous deployment after an atomic
  # start failure. A second rollback must not delete that restored deployment.
  exit 0
fi
if [ -x "$deploy/stop.sh" ]; then "$deploy/stop.sh" || true; fi
rm -rf "$deploy"
mv "$previous" "$deploy"
"$deploy/start.sh" || true
`

const finalizeStorageScript = `set -eu
rm -rf "$HOME/moox/storage.previous"
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
  case "$reset_data" in 0|1) ;; *) echo 'control_reset_invalid' >&2; return 1 ;; esac
  case "$tls_mode" in public|internal) ;; *) echo 'control_tls_mode_invalid' >&2; return 1 ;; esac
  case "$activation_token" in *[!A-Za-z0-9._-]*|'') echo 'control_activation_token_invalid' >&2; return 1 ;; esac
  [ "$archive" = "/tmp/moox-control-$activation_token.tar.gz" ] || {
    echo 'control_archive_invalid' >&2
    return 1
  }
  root="$HOME/moox"
  deploy="$root/prod"
  next="$root/prod.next"
  previous="$root/prod.previous.$activation_token"
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
    cron_line='* * * * * PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin $HOME/moox/prod/healthcheck.sh >/dev/null 2>&1'
    current=$("$crontab_command" -l 2>/dev/null || true)
    {
      printf '%s\n' "$current" | grep -Fv '/moox/prod/healthcheck.sh' || true
      printf '%s\n' "$cron_line"
    } | "$crontab_command" -
  }
  # Install the scheduler before any deployment mutation. If cron is
  # unavailable, the existing deployment remains untouched.
  install_control_healthcheck_cron
  rm -rf "$next" "$previous"
  mkdir -p "$next"
  tar -C "$next" -xzf "$archive"
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
  if [ -f "$next/secrets/msgbox.env.next" ]; then
    mv -f "$next/secrets/msgbox.env.next" "$next/secrets/msgbox.env"
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
    if [ -d "$previous" ]; then
      mv "$previous" "$deploy"
      "$deploy/start.sh" 8>&- || true
      [ ! -x "$deploy/lib/caddy-managed.sh" ] || "$deploy/lib/caddy-managed.sh" start --deploy-dir "$deploy" --os linux --arch "$target_arch" 8>&- || true
    fi
    caddy_stopped=0
    return 1
  fi
  if ! "$deploy/start.sh" 8>&-; then
    if [ -x "$deploy/lib/caddy-managed.sh" ] && ! "$deploy/lib/caddy-managed.sh" stop --deploy-dir "$deploy" --os linux --arch "$target_arch"; then
      echo 'managed Caddy could not be stopped; leaving the failed deployment in place for safe retry' >&2
      return 1
    fi
    "$deploy/stop.sh" || true
    rm -rf "$deploy"
    if [ -d "$previous" ]; then
      mv "$previous" "$deploy"
      "$deploy/start.sh" 8>&- || true
      [ ! -x "$deploy/lib/caddy-managed.sh" ] || "$deploy/lib/caddy-managed.sh" start --deploy-dir "$deploy" --os linux --arch "$target_arch" 8>&- || true
    fi
    caddy_stopped=0
    return 1
  fi
  caddy_stopped=0
}
install_control "$1" "$2" "$3" "$4" "$5" "$6" "$7"`

const rollbackControlScript = `set -eu
deploy="$HOME/moox/prod"
activation_token="$1"
case "$activation_token" in *[!A-Za-z0-9._-]*|'') echo 'control_activation_token_invalid' >&2; exit 1 ;; esac
previous="$HOME/moox/prod.previous.$activation_token"
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
if [ -d "$previous" ]; then
  mv "$previous" "$deploy"
  "$deploy/start.sh" 8>&-
  [ ! -x "$deploy/lib/caddy-managed.sh" ] || "$deploy/lib/caddy-managed.sh" start --deploy-dir "$deploy" --os linux --arch "$(uname -m)" 8>&-
fi`

const finalizeControlScript = `set -eu
deploy="$HOME/moox/prod"
activation_token="$1"
case "$activation_token" in *[!A-Za-z0-9._-]*|'') echo 'control_activation_token_invalid' >&2; exit 1 ;; esac
flock_command="${MOOX_FLOCK_COMMAND:-flock}"
command -v "$flock_command" >/dev/null 2>&1 || { echo 'control_maintenance_lock_unavailable' >&2; exit 1; }
exec 8>"$deploy.maintenance.lock"
"$flock_command" 8
[ -s "$deploy/.control-activation-token" ] || exit 0
[ "$(cat "$deploy/.control-activation-token")" = "$activation_token" ] || exit 0
rm -rf "$HOME/moox"/prod.previous.*
rm -f "$deploy/.control-activation-token"`
