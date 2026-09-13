package command

import (
	"context"
	"io"
	"io/fs"
	"net"
	"path"
	"strings"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/stretchr/testify/require"
)

func TestRequireDedicatedFactorEngineDeployDir(t *testing.T) {
	t.Parallel()
	require.Error(t, requireDedicatedFactorEngineDeployDir(""))
	require.Error(t, requireDedicatedFactorEngineDeployDir(setupconfig.DefaultControlRoot))
	require.Error(t, requireDedicatedFactorEngineDeployDir(setupconfig.DefaultStorageRoot+"/factor-engine"))
	require.NoError(t, requireDedicatedFactorEngineDeployDir(defaultFactorEngineDeployDir))
}

func TestFactorEngineStorageRPCUsesPublicGateway(t *testing.T) {
	t.Parallel()
	snapshot := setupSnapshot(t)
	snapshot.Manifest.SCFFetcher.Spaces = []setupconfig.SCFFetcherSpace{{
		StorageGatewayNodeID:           "storage",
		StorageGatewayHost:             "203.0.113.20",
		StoragePrivateRPCGatewayTarget: "ip://10.0.0.5:11003",
	}}
	target, nodeID, err := factorEngineStorageRPC(snapshot)
	require.NoError(t, err)
	require.Equal(t, "ip://203.0.113.20:11003", target)
	require.Equal(t, "storage", nodeID)
}

func TestInstallFactorEngineRuntimeUploadsControlMaterial(t *testing.T) {
	snapshot := setupSnapshot(t)
	snapshot.Manifest.Paths.ControlRoot = "/data/moox/prod"
	snapshot.Manifest.SCFFetcher.Spaces = []setupconfig.SCFFetcherSpace{{
		StorageGatewayNodeID:           "storage",
		StorageGatewayHost:             "203.0.113.20",
		StoragePrivateRPCGatewayTarget: "ip://10.0.0.5:11003",
	}}
	control := &factorEngineSSH{
		home: "/home/ubuntu",
		files: map[string][]byte{
			"/home/ubuntu/.config/moox/eventbus/factor-engine-eventbus.yaml": []byte("urls:\n  - tls://203.0.113.8:4222\n"),
			"/home/ubuntu/.config/moox/eventbus/ca.pem":                      []byte("ca"),
			"/data/moox/prod/secrets/gateway-factor.key":                     []byte("gateway-secret"),
			"/data/moox/prod/secrets/storage-internal-auth.env":              []byte("MOOX_STORAGE_PRIMARY_AUTH_SECRET=p\nMOOX_STORAGE_VIEW_AUTH_SECRET=v\n"),
			"/data/moox/prod/secrets/health-auth.env":                        []byte("MOOX_HEALTH_AUTH_ACCESS_KEY=ak\nMOOX_HEALTH_AUTH_SECRET_KEY=sk\n"),
		},
	}
	target := &factorEngineSSH{home: "/home/mooyang", uploads: map[string][]byte{}}
	require.NoError(t, installFactorEngineRuntime(t.Context(), snapshot, target, control))
	require.Equal(t, []byte("urls:\n  - tls://203.0.113.8:4222\n"), target.uploads["/home/mooyang/.config/moox/eventbus/factor-engine-eventbus.yaml"])
	require.Equal(t, []byte("ca"), target.uploads["/home/mooyang/.config/moox/eventbus/ca.pem"])
	require.Equal(t, []byte("gateway-secret"), target.uploads["/home/mooyang/.config/moox/factor-engine/gateway-factor.key"])
	require.Contains(t, string(target.uploads["/home/mooyang/.config/moox/factor-engine/runtime.env"]), "MOOX_FACTOR_STORAGE_RPC_GATEWAY_TARGET=ip://203.0.113.20:11003")
	require.Contains(t, string(target.uploads["/home/mooyang/.config/moox/factor-engine/runtime.env"]), "MOOX_FACTOR_STORAGE_RPC_GATEWAY_NODE_ID=storage")
	require.Equal(t, fs.FileMode(0o600), target.modes["/home/mooyang/.config/moox/eventbus/factor-engine-eventbus.yaml"])
	require.True(t, target.mkdirs)
}

func TestInstallFactorEngineRuntimeRejectsLoopbackEventBusURL(t *testing.T) {
	snapshot := setupSnapshot(t)
	snapshot.Manifest.SCFFetcher.Spaces = []setupconfig.SCFFetcherSpace{{
		StorageGatewayNodeID:           "storage",
		StorageGatewayHost:             "203.0.113.20",
		StoragePrivateRPCGatewayTarget: "ip://10.0.0.5:11003",
	}}
	control := &factorEngineSSH{
		home: "/home/ubuntu",
		files: map[string][]byte{
			"/home/ubuntu/.config/moox/eventbus/factor-engine-eventbus.yaml": []byte("urls:\n  - nats://127.0.0.1:4222\n"),
		},
	}
	err := installFactorEngineRuntime(t.Context(), snapshot, &factorEngineSSH{home: "/home/mooyang"}, control)
	require.EqualError(t, err, "factor-engine EventBus URL must be tls://")
}

func TestInstallFactorEngineRuntimeRejectsLoopbackTLSURL(t *testing.T) {
	snapshot := setupSnapshot(t)
	snapshot.Manifest.SCFFetcher.Spaces = []setupconfig.SCFFetcherSpace{{
		StorageGatewayNodeID:           "storage",
		StorageGatewayHost:             "203.0.113.20",
		StoragePrivateRPCGatewayTarget: "ip://10.0.0.5:11003",
	}}
	control := &factorEngineSSH{
		home: "/home/ubuntu",
		files: map[string][]byte{
			"/home/ubuntu/.config/moox/eventbus/factor-engine-eventbus.yaml": []byte("urls:\n  - tls://127.0.0.1:4222\n"),
		},
	}
	err := installFactorEngineRuntime(t.Context(), snapshot, &factorEngineSSH{home: "/home/mooyang"}, control)
	require.EqualError(t, err, "factor-engine EventBus URL must not be loopback")
}

type factorEngineSSH struct {
	home    string
	files   map[string][]byte
	uploads map[string][]byte
	modes   map[string]fs.FileMode
	mkdirs  bool
}

func (f *factorEngineSSH) Check(context.Context) error { return nil }
func (f *factorEngineSSH) ForwardLocal(context.Context, string) (net.Listener, error) {
	return nil, nil
}
func (f *factorEngineSSH) Upload(_ context.Context, src io.Reader, _ int64, dst string, mode fs.FileMode) error {
	if f.uploads == nil {
		f.uploads = map[string][]byte{}
	}
	if f.modes == nil {
		f.modes = map[string]fs.FileMode{}
	}
	payload, err := io.ReadAll(src)
	if err != nil {
		return err
	}
	f.uploads[dst] = payload
	f.modes[dst] = mode
	return nil
}
func (f *factorEngineSSH) Run(_ context.Context, argv []string, _ io.Reader) (setupssh.Result, error) {
	command := strings.Join(argv, " ")
	switch {
	case strings.Contains(command, `printf '%s' "$HOME"`):
		return setupssh.Result{Stdout: f.home}, nil
	case strings.Contains(command, "mkdir -p"):
		f.mkdirs = true
		return setupssh.Result{}, nil
	case strings.Contains(command, "cat "):
		name := argv[len(argv)-1]
		if !path.IsAbs(name) {
			name = path.Join(f.home, name)
		}
		data, ok := f.files[name]
		if !ok {
			return setupssh.Result{ExitCode: 1}, fs.ErrNotExist
		}
		return setupssh.Result{Stdout: string(data)}, nil
	default:
		return setupssh.Result{}, nil
	}
}
func (f *factorEngineSSH) Close() error { return nil }

func TestIsFactorEngineService(t *testing.T) {
	t.Parallel()
	require.True(t, isFactorEngineService("factor-engine"))
	require.True(t, isFactorEngineService("moox-factor-engine"))
	require.False(t, isFactorEngineService("factor"))
}
