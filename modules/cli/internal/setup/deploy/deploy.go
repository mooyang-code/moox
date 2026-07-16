package deploy

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
)

const remoteArchiveNext = "/tmp/moox-control.tar.gz.next"

type Options struct {
	RepositoryRoot string
	PublicHost     string
	BrowserPort    int
	TargetGOOS     string
	TargetGOARCH   string
}

type Packager interface {
	Package(context.Context, Options) (string, error)
}

type ReadinessStage string

const (
	AdminReady        ReadinessStage = "admin_ready"
	SetupReady        ReadinessStage = "setup_ready"
	GatewayReady      ReadinessStage = "gateway_ready"
	WebReady          ReadinessStage = "web_ready"
	BrowserHTTPSReady ReadinessStage = "browser_https_ready"
)

type Probe interface {
	Wait(context.Context, setupssh.Client, ReadinessStage, Options) error
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
	if err := transport.Upload(ctx, file, info.Size(), remoteArchiveNext, fs.FileMode(0o600)); err != nil {
		_ = file.Close()
		return fmt.Errorf("control_upload_failed")
	}
	_ = file.Close()
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = transport.Run(cleanupCtx, []string{"rm", "-f", remoteArchiveNext}, nil)
	}()

	if _, err := transport.Run(ctx, []string{
		"sh", "-lc", installControlScript, "moox-install-control",
		opts.PublicHost, strconv.Itoa(opts.BrowserPort), opts.TargetGOARCH,
	}, nil); err != nil {
		return fmt.Errorf("control_install_failed")
	}
	installed := true
	defer func() {
		if returnErr != nil && installed {
			rollbackCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, _ = transport.Run(rollbackCtx, []string{"sh", "-lc", rollbackControlScript}, nil)
		}
	}()
	for _, stage := range []ReadinessStage{AdminReady, SetupReady, GatewayReady, WebReady, BrowserHTTPSReady} {
		if err := deps.Probe.Wait(ctx, transport, stage, opts); err != nil {
			return fmt.Errorf("control_deploy_not_ready")
		}
	}
	ca, err := transport.Run(ctx, []string{"sh", "-lc", `cat "$HOME/moox/prod/certs/caddy/root.crt"`}, nil)
	if err != nil || deps.CAStore.Save(opts.PublicHost, []byte(ca.Stdout)) != nil {
		return fmt.Errorf("control_ca_unavailable")
	}
	if _, err := transport.Run(ctx, []string{"sh", "-lc", finalizeControlScript}, nil); err != nil {
		return fmt.Errorf("control_finalize_failed")
	}
	installed = false
	return nil
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
		"--node-id", "control", "--gateway-control-url", "http://127.0.0.1:11000",
	)
	command.Dir = root
	if err := command.Run(); err != nil {
		_ = os.Remove(archive)
		return "", err
	}
	return archive, nil
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
	args := []string{"sh", "-lc", command, "moox-readiness", opts.PublicHost, strconv.Itoa(opts.BrowserPort)}
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
	case WebReady:
		return `"$HOME/moox/prod/status.sh" web-host >/dev/null`
	case BrowserHTTPSReady:
		return `curl -fsS --cacert "$HOME/moox/prod/certs/caddy/root.crt" "https://$1:$2/" >/dev/null`
	default:
		return "false"
	}
}

// The script is constant. Positional arguments contain only public deployment metadata.
const installControlScript = `set -eu
install_control() {
  public_host="$1"
  browser_port="$2"
  target_arch="$3"
  root="$HOME/moox"
  deploy="$root/prod"
  next="$root/prod.next"
  previous="$root/prod.previous"
  archive=/tmp/moox-control.tar.gz.next
  rm -rf "$next" "$previous"
  mkdir -p "$next"
  tar -C "$next" -xzf "$archive"
  if [ -d "$deploy/data" ]; then cp -R "$deploy/data/." "$next/data/"; fi
  if [ -d "$deploy/secrets" ]; then cp -R "$deploy/secrets/." "$next/secrets/"; fi
  mkdir -p "$HOME/.config/moox/credentials" "$next/secrets"
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
    secret=$("$next/bin/moox-admin-cli" random-secret --bytes 32 --label health | sed -n 's/.*"secret":"\([^"]*\)".*/\1/p')
    umask 077
    printf 'MOOX_HEALTH_AUTH_VERSION=moox-health-v1\nMOOX_HEALTH_AUTH_ACCESS_KEY=monitor\nMOOX_HEALTH_AUTH_SECRET_KEY=%s\n' "$secret" >"$next/secrets/health-auth.env"
  fi
  if [ ! -s "$next/secrets/admin-jwt.env" ]; then
    secret=$("$next/bin/moox-admin-cli" random-secret --bytes 32 --label admin-jwt | sed -n 's/.*"secret":"\([^"]*\)".*/\1/p')
    umask 077
    printf 'MOOX_ADMIN_JWT_SECRET_KEY=%s\n' "$secret" >"$next/secrets/admin-jwt.env"
  fi
  chmod 600 "$next/secrets/"*
  if [ -x "$deploy/stop.sh" ] && ! "$deploy/stop.sh"; then "$deploy/start.sh" || true; return 1; fi
  if [ -d "$deploy" ]; then mv "$deploy" "$previous"; fi
  mv "$next" "$deploy"
  if ! MOOX_PUBLIC_HOST="$public_host" MOOX_BROWSER_HTTPS_PORT="$browser_port" MOOX_SERVICE_HTTPS_PORT=11001 \
    MOOX_CADDY_CHECKSUMS="$deploy/lib/caddy-v2.11.4-checksums.txt" \
    MOOX_CADDY_ARCHIVE="$deploy/lib/caddy_2.11.4_linux_${target_arch}.tar.gz" \
    "$deploy/lib/caddy-managed.sh" ensure --deploy-dir "$deploy" --os linux --arch "$target_arch" \
      --ports "$browser_port,11001" --config "$deploy/config/caddy/Caddyfile.next"; then
    rm -rf "$deploy"
    if [ -d "$previous" ]; then mv "$previous" "$deploy"; "$deploy/start.sh" || true; fi
    return 1
  fi
  if ! "$deploy/start.sh"; then
    "$deploy/stop.sh" || true
    rm -rf "$deploy"
    if [ -d "$previous" ]; then mv "$previous" "$deploy"; "$deploy/start.sh" || true; fi
    return 1
  fi
}
install_control "$1" "$2" "$3"`

const rollbackControlScript = `set -eu
deploy="$HOME/moox/prod"
previous="$HOME/moox/prod.previous"
if [ -x "$deploy/stop.sh" ]; then "$deploy/stop.sh" || true; fi
rm -rf "$deploy"
if [ -d "$previous" ]; then mv "$previous" "$deploy"; "$deploy/start.sh"; fi`

const finalizeControlScript = `rm -rf "$HOME/moox/prod.previous"`
