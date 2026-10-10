//go:build linux

package unitdeploy

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitbootstrap"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitinstall"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitpackage"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"github.com/pkg/sftp"
	"github.com/stretchr/testify/require"
	xssh "golang.org/x/crypto/ssh"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
)

// This server uses real SSH authentication, host keys, SFTP, and target shell
// execution. Linux gates run it in an isolated network namespace with actual
// prebuilt core, host, business and moox-runtime binaries.
func nativeSSH(t *testing.T) (setupssh.Target, string) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := xssh.NewSignerFromKey(key)
	require.NoError(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)
	target := setupssh.Target{Name: "control", Address: "127.0.0.1", Port: port, Username: "fixture"}
	config := &xssh.ServerConfig{PasswordCallback: func(metadata xssh.ConnMetadata, password []byte) (*xssh.Permissions, error) {
		if metadata.User() == "fixture" && string(password) == "synthetic-ssh-password" {
			return nil, nil
		}
		return nil, fmt.Errorf("denied")
	}}
	config.AddHostKey(signer)
	var connections sync.WaitGroup
	connections.Add(1)
	go func() {
		defer connections.Done()
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			connections.Add(1)
			go func() {
				defer connections.Done()
				connection, channels, requests, err := xssh.NewServerConn(raw, config)
				if err != nil {
					raw.Close()
					return
				}
				defer connection.Close()
				go xssh.DiscardRequests(requests)
				var sessions sync.WaitGroup
				defer sessions.Wait()
				for incoming := range channels {
					if incoming.ChannelType() == "direct-tcpip" {
						var destination struct {
							Address    string
							Port       uint32
							Origin     string
							OriginPort uint32
						}
						if xssh.Unmarshal(incoming.ExtraData(), &destination) != nil {
							incoming.Reject(xssh.ConnectionFailed, "invalid destination")
							continue
						}
						upstream, err := net.Dial("tcp", net.JoinHostPort(destination.Address, strconv.Itoa(int(destination.Port))))
						if err != nil {
							incoming.Reject(xssh.ConnectionFailed, "unreachable")
							continue
						}
						channel, requests, err := incoming.Accept()
						if err != nil {
							upstream.Close()
							continue
						}
						go xssh.DiscardRequests(requests)
						sessions.Add(1)
						go func() {
							defer sessions.Done()
							done := make(chan struct{}, 2)
							go func() { io.Copy(upstream, channel); done <- struct{}{} }()
							go func() { io.Copy(channel, upstream); done <- struct{}{} }()
							<-done
							channel.Close()
							upstream.Close()
							<-done
						}()
						continue
					}
					if incoming.ChannelType() != "session" {
						incoming.Reject(xssh.UnknownChannelType, "unsupported")
						continue
					}
					channel, requests, err := incoming.Accept()
					if err != nil {
						continue
					}
					sessions.Add(1)
					go func() {
						defer sessions.Done()
						defer channel.Close()
						for request := range requests {
							switch request.Type {
							case "exec":
								var input struct{ Command string }
								if xssh.Unmarshal(request.Payload, &input) != nil {
									request.Reply(false, nil)
									continue
								}
								request.Reply(true, nil)
								command := exec.CommandContext(t.Context(), "sh", "-c", input.Command)
								command.Stdin, command.Stdout, command.Stderr = channel, channel, channel.Stderr()
								status := uint32(0)
								if err := command.Run(); err != nil {
									status = 1
								}
								channel.SendRequest("exit-status", false, xssh.Marshal(struct{ Status uint32 }{status}))
								return
							case "subsystem":
								var input struct{ Name string }
								if xssh.Unmarshal(request.Payload, &input) != nil || input.Name != "sftp" {
									request.Reply(false, nil)
									continue
								}
								request.Reply(true, nil)
								server, err := sftp.NewServer(channel)
								if err == nil {
									server.Serve()
									server.Close()
								}
								return
							default:
								request.Reply(false, nil)
							}
						}
					}()
				}
			}()
		}
	}()
	t.Cleanup(func() { listener.Close(); connections.Wait() })
	return target, xssh.FingerprintSHA256(signer.PublicKey())
}

func copyArtifact(t *testing.T, source, destination string, mode os.FileMode) {
	t.Helper()
	input, err := os.Open(source)
	require.NoError(t, err)
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	require.NoError(t, err)
	_, err = io.Copy(output, input)
	require.NoError(t, err)
	require.NoError(t, output.Sync())
	require.NoError(t, output.Close())
}

func TestNativeCoreBootstrapOverVerifiedSSHWithFullFleetTopology(t *testing.T) {
	if os.Getenv("MOOX_BOOTSTRAP_HOST_ARCHIVE") == "" || os.Getenv("MOOX_CORE_SOURCE_ROOT") == "" {
		t.Skip("requires actual prebuilt Linux artifacts; mandatory in native core Linux gate")
	}
	root, err := os.MkdirTemp("", "moox-native-core-")
	require.NoError(t, err)
	t.Cleanup(func() {
		if !t.Failed() {
			os.RemoveAll(root)
		} else {
			t.Logf("synthetic diagnostics: %s", root)
		}
	})
	target, fingerprint := nativeSSH(t)
	deployment := filepath.Join(root, "deployment")
	config := fmt.Sprintf(`[admin]
username = "admin"
password = "synthetic-admin-password"
[tencent_cloud]
secret_id = "synthetic-cloud-id"
secret_key = "synthetic-cloud-secret"
[eventbus]
port = 4222
tls_enabled = true
[paths]
deploy_root = %q
control_root = %q
storage_root = %q
[hosts.control]
address = "127.0.0.1"
ssh = {username="fixture",password="synthetic-ssh-password",port=%d}
[hosts.storage]
address = "127.0.0.2"
ssh = {username="fixture",password="synthetic-ssh-password"}
[hosts.compute1]
address = "127.0.0.3"
ssh = {username="fixture",password="synthetic-ssh-password"}
[placements]
control = ["admin","eventbus","console-proxy","web-host","monitor","collector","cloudnode","factor-mgr","strategy","access","egress-proxy","trade","storage-primary","storage-node","storage-view"]
storage = ["access"]
compute1 = ["access"]
`, deployment, filepath.Join(deployment, "units", "prod"), filepath.Join(deployment, "storage"), target.Port)
	configPath := filepath.Join(root, "moox.toml")
	require.NoError(t, os.WriteFile(configPath, []byte(config), 0o600))
	snapshot, err := setupconfig.Load(configPath, root)
	require.NoError(t, err)
	options := CoreOptions{RepositoryRoot: os.Getenv("MOOX_CORE_SOURCE_ROOT"), BinaryDirectory: filepath.Join(root, "bin"), StateDirectory: filepath.Join(root, "operator-state"), OperatorDirectory: filepath.Join(root, "operator-config"), SSH: setupssh.Options{KnownHostsPath: filepath.Join(root, "known_hosts"), DisableAgent: true}}
	require.NoError(t, os.WriteFile(options.SSH.KnownHostsPath, nil, 0o600))
	require.NoError(t, os.Mkdir(options.BinaryDirectory, 0o700))
	for i, source := range []string{os.Getenv("MOOX_BOOTSTRAP_HOST_ARCHIVE"), os.Getenv("MOOX_BOOTSTRAP_CONTROL_ARCHIVE")} {
		item, err := unitpackage.Inspect(t.Context(), source)
		require.NoError(t, err)
		extraction, err := unitpackage.Extract(t.Context(), unitpackage.ExtractOptions{Archive: source, Destination: filepath.Join(root, fmt.Sprintf("prebuilt-%d", i)), ExpectedSHA256: item.SHA256, Profile: item.Manifest.Profile, GOOS: "linux", GOARCH: runtime.GOARCH})
		require.NoError(t, err)
		binaries, err := unitpackage.Binaries(item.Manifest.Profile)
		require.NoError(t, err)
		for _, binary := range binaries {
			copyArtifact(t, filepath.Join(extraction.Directory, "bin", binary), filepath.Join(options.BinaryDirectory, binary), 0o700)
		}
	}
	_, err = BootstrapCore(t.Context(), snapshot, options)
	require.ErrorIs(t, err, setupssh.ErrHostKeyUnknown)
	_, err = os.Stat(deployment)
	require.True(t, os.IsNotExist(err), "an untrusted host must never receive deployment input")
	require.NoError(t, setupssh.TrustHost(t.Context(), target, fingerprint, options.SSH))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		for _, directory := range []string{filepath.Join(deployment, "trade"), filepath.Join(deployment, "egress-proxy"), filepath.Join(deployment, "access"), filepath.Join(deployment, "storage"), snapshot.Manifest.Paths.ControlRoot, filepath.Join(deployment, "host")} {
			plan := filepath.Join(directory, "current", "runtime.json")
			if _, err := os.Stat(plan); err == nil {
				output, err := exec.CommandContext(ctx, os.Getenv("MOOX_RUNTIME_BINARY"), "stop", "--plan", plan).CombinedOutput()
				require.NoError(t, err, "%s", output)
			}
		}
	})
	initial, err := BootstrapCore(t.Context(), snapshot, options)
	require.NoError(t, err)
	t.Log("actual core services ready")
	require.Equal(t, "core-ready", initial.Stage)
	require.Equal(t, filepath.Join(snapshot.Manifest.Paths.ControlRoot, "releases"), filepath.Dir(initial.Bootstrap.ControlDirectory))
	_, err = os.Stat(filepath.Join(deployment, "control"))
	require.True(t, os.IsNotExist(err), "custom control root must not create a second default unit")
	passwordHash := nativeAdminPasswordHash(t, initial.Bootstrap.ControlDirectory, snapshot.Manifest.Admin.Username)
	require.True(t, mooxsecurity.VerifyPassword(snapshot.Manifest.Admin.Password, passwordHash))
	nativeAdminLogin(t, snapshot.Manifest.Admin.Username, snapshot.Manifest.Admin.Password)
	status := func(directory string) unitruntime.Result {
		output, err := exec.CommandContext(t.Context(), os.Getenv("MOOX_RUNTIME_BINARY"), "status", "--plan", filepath.Join(directory, "runtime.json")).CombinedOutput()
		require.NoError(t, err, "%s", output)
		var result unitruntime.Result
		require.NoError(t, json.Unmarshal(output, &result))
		return result
	}
	host, control := status(initial.Bootstrap.HostDirectory), status(initial.Bootstrap.ControlDirectory)
	require.Len(t, host.Components, 2)
	require.Len(t, control.Components, 2)
	for _, item := range append(host.Components, control.Components...) {
		require.True(t, item.Ready, item.ID)
		require.Positive(t, item.PID)
	}
	_, err = os.Stat(filepath.Join(deployment, "identity", "console-proxy"))
	require.True(t, os.IsNotExist(err), "core staging must not consume proxy CA creation authorization")
	operator, err := os.ReadFile(initial.OperatorConfig)
	require.NoError(t, err)
	var identity struct {
		Caller  string `yaml:"caller"`
		KeyID   string `yaml:"key_id"`
		KeyFile string `yaml:"key_file"`
	}
	require.NoError(t, yaml.Unmarshal(operator, &identity))
	require.Equal(t, "moox-cli", identity.Caller)
	require.NotEmpty(t, identity.KeyID)
	_, err = gatewayauth.ReadSigningSecret(identity.KeyFile)
	require.NoError(t, err)
	files, err := os.ReadDir(options.OperatorDirectory)
	require.NoError(t, err)
	require.Len(t, files, 3)
	for _, entry := range files {
		require.False(t, strings.Contains(entry.Name(), "server"))
		require.False(t, strings.Contains(entry.Name(), "admin"))
	}
	options.RepositoryRoot, options.BinaryDirectory = "", ""
	repeated, err := BootstrapCore(t.Context(), snapshot, options)
	require.NoError(t, err)
	require.Equal(t, initial, repeated)
	require.Equal(t, passwordHash, nativeAdminPasswordHash(t, repeated.Bootstrap.ControlDirectory, snapshot.Manifest.Admin.Username), "a completed retry must not rehash or reset the administrator password")
	require.Equal(t, host, status(initial.Bootstrap.HostDirectory))
	require.Equal(t, control, status(initial.Bootstrap.ControlDirectory))
	// Losing native state cannot replace credentials or restart a healthy
	// target. Prebuilt input is explicit; this gate never invokes a compiler.
	options.StateDirectory = filepath.Join(root, "lost-native-state")
	options.RepositoryRoot, options.BinaryDirectory = os.Getenv("MOOX_CORE_SOURCE_ROOT"), filepath.Join(root, "bin")
	_, err = BootstrapCore(t.Context(), snapshot, options)
	require.ErrorContains(t, err, "original state directory")
	require.Equal(t, host, status(initial.Bootstrap.HostDirectory))
	require.Equal(t, control, status(initial.Bootstrap.ControlDirectory))
	// Private input and successful helper output cannot leak runtime secrets.
	encoded, err := json.Marshal(initial)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "synthetic-admin-password")
	require.NotContains(t, string(encoded), "synthetic-ssh-password")
	// Exercise the native host entry against the running control host. Other
	// hosts remain registered; this test does not claim multi-host acceptance.
	_, err = DeployHost(t.Context(), snapshot, HostOptions{CoreOptions: options, HostID: "control"})
	require.ErrorContains(t, err, "original core state")
	options.StateDirectory = filepath.Join(root, "operator-state")
	deployed, err := DeployHost(t.Context(), snapshot, HostOptions{CoreOptions: options, HostID: "control"})
	require.NoError(t, err)
	t.Log("native host deployment ready")
	require.Equal(t, "host-ready", deployed.Stage)
	require.NotEqual(t, initial.Bootstrap.HostDirectory, deployed.Deployment.Directory)
	host = status(deployed.Deployment.Directory)
	require.Equal(t, control, status(initial.Bootstrap.ControlDirectory))
	state, err := privateRoot(filepath.Join(options.StateDirectory, "hosts/control"))
	require.NoError(t, err)
	var operation unitOperation
	require.NoError(t, readJSON(state, "operation.json", 2<<20, &operation))
	state.Close()
	exported, err := unitbootstrap.ExportHost(t.Context(), operation.Source)
	require.NoError(t, err)
	require.Equal(t, *operation.Export, exported)
	// Losing only the final public checkpoint resumes the already issued
	// host and EventBus exports without changing certificates or role tokens.
	exportFile := filepath.Join(deployment, "material-exports", operation.Source.ExportID, "export.json")
	require.NoError(t, os.Remove(exportFile))
	exported, err = unitbootstrap.ExportHost(t.Context(), operation.Source)
	require.NoError(t, err)
	require.Equal(t, *operation.Export, exported)
	changed := operation.Source
	changed.Roles = []string{"metrics-publisher"}
	_, err = unitbootstrap.ExportHost(t.Context(), changed)
	require.ErrorContains(t, err, "original request")
	t.Log("source export checkpoints reused without credential changes")
	for _, component := range host.Components {
		require.True(t, component.Ready)
		require.Positive(t, component.PID)
	}
	retry, err := DeployHost(t.Context(), snapshot, HostOptions{CoreOptions: options, HostID: "control"})
	require.NoError(t, err)
	require.Equal(t, deployed, retry)
	require.Equal(t, host, status(deployed.Deployment.Directory))
	plan := filepath.Join(deployed.Deployment.Directory, "runtime.json")
	lifecycle := func(operation string) {
		t.Helper()
		output, err := exec.CommandContext(t.Context(), os.Getenv("MOOX_RUNTIME_BINARY"), operation, "--plan", plan, "--components", "host-agent").CombinedOutput()
		require.NoError(t, err, "%s", output)
	}
	lifecycle("pause")
	paused, err := DeployHost(t.Context(), snapshot, HostOptions{CoreOptions: options, HostID: "control"})
	require.NoError(t, err)
	require.Equal(t, "host-paused", paused.Stage)
	for _, component := range paused.Deployment.Components {
		if component.ID == "host-agent" {
			require.Equal(t, "paused", component.State)
		} else {
			require.Equal(t, host.Components[0].PID, component.PID)
		}
	}
	lifecycle("resume")
	lifecycle("stop")
	repaired, err := DeployHost(t.Context(), snapshot, HostOptions{CoreOptions: options, HostID: "control"})
	require.NoError(t, err)
	require.Equal(t, "host-ready", repaired.Stage)
	require.Equal(t, deployed.Deployment.Directory, repaired.Deployment.Directory)
	require.Equal(t, host.Components[0].PID, repaired.Deployment.Components[0].PID)
	require.Equal(t, control, status(initial.Bootstrap.ControlDirectory))
	unit, err := privateRoot(filepath.Join(deployment, "host"))
	require.NoError(t, err)
	var receipt unitinstall.Deployment
	require.NoError(t, readJSON(unit, "deployment.json", 128<<10, &receipt))
	receipt.Phase = "activating"
	require.NoError(t, saveJSON(unit, "deployment.json", receipt, true))
	resumed, err := DeployHost(t.Context(), snapshot, HostOptions{CoreOptions: options, HostID: "control"})
	require.NoError(t, err)
	require.Equal(t, repaired.Deployment.Directory, resumed.Deployment.Directory)
	require.Equal(t, repaired.Deployment.Components, resumed.Deployment.Components)
	rollbackOutput, err := exec.CommandContext(t.Context(), os.Getenv("MOOX_RUNTIME_BINARY"), "rollback", "--unit-root", filepath.Join(deployment, "host")).CombinedOutput()
	require.NoError(t, err, "%s", rollbackOutput)
	receipt.Phase = "rolled-back"
	require.NoError(t, saveJSON(unit, "deployment.json", receipt, true))
	unit.Close()
	fresh, err := DeployHost(t.Context(), snapshot, HostOptions{CoreOptions: options, HostID: "control"})
	require.NoError(t, err)
	require.Equal(t, "host-ready", fresh.Stage)
	require.NotEqual(t, resumed.Deployment.Directory, fresh.Deployment.Directory)
	require.Equal(t, control, status(initial.Bootstrap.ControlDirectory))
	t.Log("activation checkpoint recovery and fresh candidate after rollback passed")
	if directory := os.Getenv("MOOX_BUSINESS_BINARY_DIRECTORY"); directory != "" {
		for _, binary := range []string{"moox-access", "moox-egress-proxy", "moox-trade", "moox-trade-cli", "moox-storage-primary", "moox-storage-node", "moox-storage-view", "moox-storage-cli"} {
			copyArtifact(t, filepath.Join(directory, binary), filepath.Join(root, "bin", binary), 0o700)
		}
		options.RepositoryRoot, options.BinaryDirectory = os.Getenv("MOOX_CORE_SOURCE_ROOT"), filepath.Join(root, "bin")
		for _, profile := range []string{"access", "egress-proxy", "trade", "storage"} {
			deployed, err := DeployUnit(t.Context(), snapshot, UnitOptions{CoreOptions: options, HostID: "control", Profile: profile})
			require.NoError(t, err, profile)
			require.Equal(t, profile+"-ready", deployed.Stage)
			prepared, err := unitinstall.ReadInstalled(t.Context(), deployed.Deployment.Directory)
			require.NoError(t, err)
			components, err := unitComponents(snapshot.Manifest, "control", profile)
			require.NoError(t, err)
			require.Len(t, prepared.Identity.Credentials, len(components))
			for _, credential := range prepared.Identity.Credentials {
				require.Contains(t, components, credential.Caller)
			}
			if profile == "storage" {
				require.FileExists(t, filepath.Join(deployed.Deployment.Directory, "storage-primary/var/storage/metadata/storage_metadata.db"))
			}
			repeated, err := DeployUnit(t.Context(), snapshot, UnitOptions{CoreOptions: options, HostID: "control", Profile: profile})
			require.NoError(t, err)
			require.Equal(t, deployed, repeated)
			require.Equal(t, control, status(initial.Bootstrap.ControlDirectory))
			t.Log("actual business unit ready with immutable retry:", profile)
		}
	} else {
		t.Fatal("native business Linux gate requires MOOX_BUSINESS_BINARY_DIRECTORY")
	}
}

func nativeAdminPasswordHash(t *testing.T, release, username string) string {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+filepath.Join(release, "admin/data/admin.db")+"?mode=ro"), &gorm.Config{})
	require.NoError(t, err)
	connection, err := db.DB()
	require.NoError(t, err)
	defer connection.Close()
	var user struct {
		Hash   string `gorm:"column:c_password_hash"`
		Role   int    `gorm:"column:c_role"`
		Status int    `gorm:"column:c_status"`
	}
	result := db.Raw("SELECT c_password_hash, c_role, c_status FROM t_users WHERE c_username = ? AND c_is_deleted = 0", username).Scan(&user)
	require.NoError(t, result.Error)
	require.EqualValues(t, 1, result.RowsAffected)
	require.Equal(t, 3, user.Role)
	require.Equal(t, 1, user.Status)
	return user.Hash
}

func nativeAdminLogin(t *testing.T, username, password string) {
	t.Helper()
	client := http.Client{Timeout: 10 * time.Second}
	call := func(method string, request, response proto.Message) {
		raw, err := protojson.Marshal(request)
		require.NoError(t, err)
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://127.0.0.1:11000/api/admin/auth/"+method, bytes.NewReader(raw))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		result, err := client.Do(req)
		require.NoError(t, err)
		defer result.Body.Close()
		require.Equal(t, http.StatusOK, result.StatusCode)
		body, err := io.ReadAll(io.LimitReader(result.Body, 128<<10))
		require.NoError(t, err)
		require.NoError(t, protojson.Unmarshal(body, response), "Admin login response must follow its protocol")
	}
	var salt adminpb.GetLoginSaltRsp
	call("GetLoginSalt", &adminpb.GetLoginSaltReq{Username: username}, &salt)
	require.NotNil(t, salt.GetRetInfo())
	require.Zero(t, salt.GetRetInfo().GetCode())
	require.NotEmpty(t, salt.GetSalt())
	encrypted, err := mooxsecurity.Encrypt(password, salt.GetSalt()+strconv.FormatInt(salt.GetTimestamp(), 10))
	require.NoError(t, err)
	var login adminpb.LoginRsp
	call("Login", &adminpb.LoginReq{Username: username, PasswordHash: encrypted, Salt: salt.GetSalt(), Timestamp: salt.GetTimestamp(), DeviceId: "native-core-gate"}, &login)
	require.NotNil(t, login.GetRetInfo())
	require.Zero(t, login.GetRetInfo().GetCode(), "the initialized administrator must be able to log in")
	require.NotEmpty(t, login.GetAccessToken())
	require.NotEmpty(t, login.GetRequestSigningKey())
}
