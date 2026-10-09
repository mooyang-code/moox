package command

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

// This preflight intentionally exposes a fixed summary, never arbitrary remote
// commands, process arguments, private keys, environment files, or log tails.
func newSetupConsoleProxyPreflightCommand(deps setupDeps) *cobra.Command {
	var file, hostName string
	cmd := &cobra.Command{
		Use:   "console-proxy-preflight",
		Short: "只读核对控制台代理迁移的端口、CA 和工具链",
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			host, err := findSetupHost(snapshot.Manifest, hostName)
			if snapshot.Manifest.HasCompileHost() && hostName == snapshot.Manifest.CompileHost.Name {
				host, err = snapshot.Manifest.CompileHost, nil
			}
			if err != nil {
				return err
			}
			transport, err := dialSetupHost(cmd.Context(), host)
			if err != nil {
				return err
			}
			defer transport.Close()
			paths := snapshot.Manifest.Paths.Resolved()
			root := paths.DeployRoot
			if host.Name == snapshot.Manifest.ControlHost.Name {
				root = paths.ControlRoot
			} else if host.Name == snapshot.Manifest.StorageHost.Name {
				root = paths.StorageRoot
			}
			result, err := transport.Run(cmd.Context(), []string{"python3", "-c", consoleProxyPreflightScript, root}, nil)
			if err != nil {
				return fmt.Errorf("console_proxy_preflight_failed: %w", err)
			}
			var summary map[string]any
			if err := json.Unmarshal([]byte(result.Stdout), &summary); err != nil {
				return fmt.Errorf("invalid console-proxy preflight summary: %w", err)
			}
			return writeSetupJSON(cmd, map[string]any{"host": host.Name, "preflight": summary})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&hostName, "host", "control", "主机名称")
	return cmd
}

const consoleProxyPreflightScript = `
import hashlib, json, os, pathlib, platform, shutil, subprocess, sys
root = pathlib.Path(sys.argv[1])
def run(args):
    try:
        result = subprocess.run(args, capture_output=True, timeout=10, env=dict(os.environ, GOTOOLCHAIN='local'))
        return result.stdout if result.returncode == 0 else b''
    except (OSError, subprocess.TimeoutExpired):
        return b''
def fingerprint(path):
    if not path.is_file(): return None
    raw = run(['openssl', 'x509', '-in', str(path), '-outform', 'DER'])
    return ':'.join('%02X' % b for b in hashlib.sha256(raw).digest()) if raw else None
storage = root / 'data/caddy/caddy'
ca = storage / 'pki/authorities/local'
published = root / 'certs/caddy/root.crt'
baseline = root / 'data/caddy/internal-ca.sha256'
pub_sha = root / 'certs/caddy/root.sha256'
def read_fingerprint(path):
    if not path.is_file(): return None
    value = path.read_text().strip().replace(':','')
    if len(value) != 64 or any(c not in '0123456789abcdefABCDEF' for c in value): return 'invalid'
    return ':'.join(value[i:i+2].upper() for i in range(0,64,2))
cert_pub = run(['openssl','x509','-in',str(ca/'root.crt'),'-noout','-pubkey']) if (ca/'root.crt').is_file() else b''
key_pub = run(['openssl','pkey','-in',str(ca/'root.key'),'-pubout']) if (ca/'root.key').is_file() else b''
processes = {}
process_locations = []
proc = pathlib.Path('/proc')
if proc.is_dir():
    for pid in proc.iterdir():
        if not pid.name.isdigit(): continue
        try:
            executable = os.readlink(pid/'exe')
            if executable.endswith(' (deleted)'): executable = executable[:-10]
            name = pathlib.Path(executable).name
        except OSError: continue
        if name in ('caddy','moox-console-proxy','moox-gateway','moox-host-gateway'):
            processes[name] = processes.get(name,0) + 1
            try: cwd = os.readlink(pid/'cwd')
            except OSError: cwd = None
            process_locations.append({'binary':name, 'executable':executable, 'working_directory':cwd})
listeners = []
for line in run(['ss','-H','-lnt']).decode(errors='replace').splitlines():
    parts = line.split()
    if len(parts) >= 4 and parts[3].rsplit(':',1)[-1] in ('9527','9528','11000','11001','11002','11003','19527','19528','2019'):
        listeners.append(parts[3])
versions = {}
go_candidates = ['go', '/usr/local/go/bin/go', '/home/ubuntu/go-sdk/bin/go', str(pathlib.Path.home()/'go-sdk/bin/go')]
go_candidates += [str(path) for path in (pathlib.Path.home()/'.local').glob('go*/bin/go')]
for path in sorted(set(go_candidates)):
    if shutil.which(path): versions[path] = run([path,'version']).decode().strip()
compilers = {}
for name in ('gcc','g++','aarch64-linux-gnu-gcc','aarch64-linux-gnu-g++'):
    if shutil.which(name): compilers[name] = run([name,'--version']).decode().splitlines()[0]
disk = shutil.disk_usage(root if root.exists() else root.parent if root.parent.exists() else '/')
print(json.dumps({
    'platform':platform.system().lower(), 'architecture':platform.machine(),
    'deploy_root_exists':root.is_dir(), 'storage_root':str(storage),
    'root_ca_sha256':fingerprint(ca/'root.crt'), 'published_ca_sha256':fingerprint(published),
    'published_fingerprint':read_fingerprint(pub_sha), 'persistent_fingerprint':read_fingerprint(baseline),
    'root_key_exists':(ca/'root.key').is_file(), 'root_key_matches':bool(cert_pub and cert_pub == key_pub),
    'root_key_private':(ca/'root.key').stat().st_mode & 0o077 == 0 if (ca/'root.key').is_file() else False,
    'intermediate_complete':(ca/'intermediate.crt').is_file() and (ca/'intermediate.key').is_file(),
    'process_counts':processes, 'process_locations':sorted(process_locations,key=lambda entry:entry['executable']),
    'listeners':sorted(listeners), 'toolchains':versions, 'c_compilers':compilers,
    'free_bytes':disk.free
},sort_keys=True))
`
