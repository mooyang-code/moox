package unitdeploy

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/failure"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
)

func targetArchitecture(ctx context.Context, transport setupssh.Client) (string, error) {
	result, err := transport.Run(ctx, []string{"python3", "-c", "import json,platform;print(json.dumps([platform.system(),platform.machine()]))"}, nil)
	if err != nil {
		return "", errors.New("target platform preflight failed")
	}
	var platform []string
	if json.Unmarshal([]byte(result.Stdout), &platform) != nil || len(platform) != 2 || platform[0] != "Linux" {
		return "", errors.New("deployment target requires Linux amd64 or arm64")
	}
	switch platform[1] {
	case "x86_64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	default:
		return "", errors.New("deployment target requires Linux amd64 or arm64")
	}
}

// Python is only a bounded file-transfer preflight. It does not read operator
// configuration, compile software, stop services, or implement lifecycle work.
const targetFiles = `import hashlib,json,os,stat,sys
op,root = sys.argv[1:3]
def syncdir(name):
    fd=os.open(name,os.O_RDONLY|os.O_DIRECTORY)
    try: os.fsync(fd)
    finally: os.close(fd)
def physical(name, private=False):
    if not os.path.isabs(name) or os.path.normpath(name)!=name or name=='/': raise ValueError('path')
    current='/'
    for part in name.split('/')[1:]:
        current=os.path.join(current,part)
        info=os.lstat(current)
        if not stat.S_ISDIR(info.st_mode): raise ValueError('directory')
    info=os.lstat(name)
    if info.st_uid!=os.getuid() or stat.S_IMODE(info.st_mode)&0o022 or private and stat.S_IMODE(info.st_mode)!=0o700: raise ValueError('owner or mode')
def mkdir(name, private=True):
    if os.path.lexists(name): physical(name,private); return
    parent=os.path.dirname(name)
    if not os.path.exists(parent): mkdir(parent,False)
    # Existing ancestors must be physical, including /tmp in test fixtures.
    current='/'
    for part in parent.split('/')[1:]:
        if not part: continue
        current=os.path.join(current,part)
        if not stat.S_ISDIR(os.lstat(current).st_mode): raise ValueError('parent')
    os.mkdir(name,0o700);syncdir(parent);physical(name,private)
try:
    if op=='prepare':
        units=sys.argv[3:]
        for unit in units:
            if not os.path.isabs(unit) or os.path.normpath(unit)!=unit or os.path.commonpath([root,unit])!=root or unit==root: raise ValueError('unit')
        mkdir(root,False)
        for unit in units: mkdir(unit,False)
        mkdir(os.path.join(root,'bootstrap-input'))
        mkdir(os.path.join(root,'staging'))
        print('{}')
    elif op=='inspect':
        filename,digest,mode = sys.argv[3:6]
        physical(root)
        base=os.path.join(root,'staging' if filename.startswith(os.path.join(root,'staging')+'/') else 'bootstrap-input')
        physical(base,True)
        if os.path.commonpath([base,filename])!=base or filename==base or os.path.normpath(filename)!=filename: raise ValueError('destination')
        relative=os.path.relpath(os.path.dirname(filename),base)
        current=base
        if relative!='.':
            for part in relative.split('/'):
                current=os.path.join(current,part);mkdir(current)
        present=os.path.lexists(filename)
        if present:
            info=os.lstat(filename)
            if not stat.S_ISREG(info.st_mode) or info.st_uid!=os.getuid() or info.st_nlink!=1 or stat.S_IMODE(info.st_mode)!=int(mode,8): raise ValueError('file')
            h=hashlib.sha256()
            with open(filename,'rb') as stream:
                while True:
                    block=stream.read(1024*1024)
                    if not block: break
                    h.update(block)
                if h.hexdigest()!=digest: raise ValueError('digest')
                os.fsync(stream.fileno())
            syncdir(os.path.dirname(filename))
        print(json.dumps({'present':present}))
    elif op=='unpack':
        import tarfile
        physical(root)
        base=os.path.join(root,'bootstrap-input')
        physical(base,True)
        wanted=json.loads(sys.argv[3])
        seen=set()
        with tarfile.open(fileobj=sys.stdin.buffer,mode='r|') as archive:
            for member in archive:
                name=os.path.normpath(os.path.join(base,member.name))
                if not member.isfile() or name not in wanted or name in seen or os.path.commonpath([base,name])!=base: raise ValueError('member')
                raw=archive.extractfile(member).read()
                if hashlib.sha256(raw).hexdigest()!=wanted[name]: raise ValueError('digest')
                relative=os.path.relpath(os.path.dirname(name),base)
                current=base
                if relative!='.':
                    for part in relative.split('/'):
                        current=os.path.join(current,part);mkdir(current)
                temporary=name+'.tmp'
                fd=os.open(temporary,os.O_WRONLY|os.O_CREAT|os.O_TRUNC,0o600)
                try:
                    with os.fdopen(os.dup(fd),'wb') as stream: stream.write(raw)
                    os.fsync(fd)
                finally: os.close(fd)
                os.replace(temporary,name);syncdir(os.path.dirname(name))
                seen.add(name)
        if seen!=set(wanted): raise ValueError('incomplete')
        print('{}')
    else: raise ValueError('operation')
except Exception:
    sys.stderr.write('private deployment transfer preflight failed\n'+'''+` + failure.Prefix + failure.TargetDirectory + `'''+'\n');sys.exit(1)
`

func prepareTarget(ctx context.Context, transport setupssh.Client, root string, units ...string) error {
	if output, err := transport.Run(ctx, append([]string{"python3", "-c", targetFiles, "prepare", root}, units...), nil); err != nil {
		return failure.Describe("检查目标机部署目录", output.ExitCode, output.Stderr)
	}
	return nil
}

func remotePresent(ctx context.Context, transport setupssh.Client, root, filename, digest string, mode fs.FileMode) (bool, error) {
	result, err := transport.Run(ctx, []string{"python3", "-c", targetFiles, "inspect", root, filename, digest, fmt.Sprintf("%o", mode.Perm())}, nil)
	if err != nil {
		return false, errors.New("target artifact verification failed; private output omitted")
	}
	var metadata struct {
		Present bool `json:"present"`
	}
	decoder := json.NewDecoder(strings.NewReader(result.Stdout))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&metadata) != nil || decoder.Decode(new(any)) != io.EOF {
		return false, errors.New("target artifact returned invalid transfer metadata")
	}
	return metadata.Present, nil
}

func uploadReader(ctx context.Context, transport setupssh.Client, root, filename, digest string, mode fs.FileMode, reader io.Reader, size int64) error {
	present, err := remotePresent(ctx, transport, root, filename, digest, mode)
	if err != nil || present {
		return err
	}
	if err := transport.Upload(ctx, reader, size, filename, mode); err != nil {
		return errors.New("verified SSH artifact upload failed; private output omitted")
	}
	present, err = remotePresent(ctx, transport, root, filename, digest, mode)
	if err != nil {
		return err
	}
	if !present {
		return errors.New("uploaded deployment artifact is missing")
	}
	return nil
}

func uploadFile(ctx context.Context, transport setupssh.Client, root, local, remote, digest string, mode fs.FileMode) error {
	actual, err := fileSHA(local)
	if err != nil || actual != digest {
		return errors.New("local deployment artifact changed before upload")
	}
	file, err := os.Open(local)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	return uploadReader(ctx, transport, root, remote, digest, mode, file, info.Size())
}

// packFile 是打包传输的一个文件。
type packFile struct {
	remote string
	raw    []byte
}

// uploadPack 把一批私有文件打成一个包，走一条 SSH 连接传完，目标机按摘要校验后以 0600 落盘。
// 逐个传时每个文件要两次校验加一次 SFTP，一个单元有二十多个文件。
func uploadPack(ctx context.Context, transport setupssh.Client, root string, files []packFile) error {
	base := path.Join(root, "bootstrap-input") + "/"
	wanted := make(map[string]string, len(files))
	var buffer bytes.Buffer
	archive := tar.NewWriter(&buffer)
	for _, file := range files {
		name, ok := strings.CutPrefix(file.remote, base)
		if !ok {
			return errors.New("deployment material must stay under bootstrap-input")
		}
		wanted[file.remote] = sha(file.raw)
		if err := archive.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(file.raw)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		if _, err := archive.Write(file.raw); err != nil {
			return err
		}
	}
	if err := archive.Close(); err != nil {
		return err
	}
	manifest, err := json.Marshal(wanted)
	if err != nil {
		return err
	}
	output, err := transport.Run(ctx, []string{"python3", "-c", targetFiles, "unpack", root, string(manifest)}, &buffer)
	if err != nil {
		return failure.Describe("传输部署材料", output.ExitCode, output.Stderr)
	}
	return nil
}

// cleanupExport 在部署成功后删掉目标机上的材料和控制机上的导出目录，它们含密钥。失败不影响部署结果。
func cleanupExport(ctx context.Context, target, control setupssh.Client, root, exportID string) {
	_, _ = target.Run(ctx, []string{"rm", "-rf", "--", path.Join(root, "bootstrap-input/host-deploy", exportID)}, nil)
	_, _ = control.Run(ctx, []string{"rm", "-rf", "--", path.Join(root, "material-exports", exportID)}, nil)
}

func uploadBytes(ctx context.Context, transport setupssh.Client, root string, raw []byte, remote string, mode fs.FileMode) error {
	return uploadReader(ctx, transport, root, remote, sha(raw), mode, bytes.NewReader(raw), int64(len(raw)))
}

// remoteFailure 把目标端助手的失败转成明确原因；operation 是这次操作的名字。
func remoteFailure(operation string, err error, output setupssh.Result) error {
	described := failure.Describe(operation, output.ExitCode, output.Stderr)
	return errors.New(described.Error() + helperDiagnostic(err, output))
}

// 断线后等目标机上的助手结束的时限和轮询间隔。助手一次最长要等多个组件的启动时限。
var (
	disconnectWait = 15 * time.Minute
	disconnectPoll = 10 * time.Second
)

// helperProbe 查目标机上是否还有部署助手在运行：助手都从上传的 runtime 目录启动，和 setup validate 的判断一致。
const helperProbe = `for e in /proc/[0-9]*/exe; do case "$(readlink "$e" 2>/dev/null)" in */bootstrap-input/runtime/*) echo running; exit 0;; esac; done; echo idle`

// helperFailure 处理部署助手的失败：目标机明确失败时直接说明原因；SSH 断开时重新连接，等助手结束再报告，
// 这样下一次重试不会和目标机上还没结束的操作重叠。
func helperFailure(ctx context.Context, redial func() (setupssh.Client, error), log io.Writer, operation string, err error, output setupssh.Result) error {
	if output.ExitCode != 0 || ctx.Err() != nil {
		return remoteFailure(operation, err, output)
	}
	if log != nil {
		fmt.Fprintf(log, "%s：SSH 连接断开，等目标机上的操作结束，最多 %s\n", operation, disconnectWait)
	}
	deadline := time.Now().Add(disconnectWait)
	for {
		if helperIdle(ctx, redial) {
			return errors.New("ssh_disconnected: " + operation + "：SSH 连接断开，目标机上的操作已结束，但没有拿到结果。运行 setup validate 查看状态，再用同样的参数重跑" + helperDiagnostic(err, output))
		}
		if time.Now().After(deadline) {
			return remoteFailure(operation, err, output)
		}
		select {
		case <-ctx.Done():
			return remoteFailure(operation, err, output)
		case <-time.After(disconnectPoll):
		}
	}
}

func helperIdle(ctx context.Context, redial func() (setupssh.Client, error)) bool {
	transport, err := redial()
	if err != nil {
		return false
	}
	defer transport.Close()
	output, err := transport.Run(ctx, []string{"sh", "-c", helperProbe}, nil)
	return err == nil && strings.TrimSpace(output.Stdout) == "idle"
}

// helperDiagnostic 在显式设置 MOOX_SETUP_DEBUG_HELPER=1 时返回目标端助手的失败输出（截断），
// 默认不输出，避免把目标端私有内容带到操作员日志。仅用于联调定位。
func helperDiagnostic(err error, output setupssh.Result) string {
	if os.Getenv("MOOX_SETUP_DEBUG_HELPER") != "1" {
		return ""
	}
	text := ""
	if err != nil {
		text = err.Error()
	}
	text += " | " + output.Stderr + " | " + output.Stdout
	if len(text) > 2000 {
		text = text[:2000]
	}
	return " [debug: " + text + "]"
}
