package unitdeploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

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
        mkdir(root,False)
        for child in ('host','control'): mkdir(os.path.join(root,child),False)
        mkdir(os.path.join(root,'bootstrap-input'))
        print('{}')
    elif op=='inspect':
        filename,digest,mode = sys.argv[3:6]
        physical(root)
        base=os.path.join(root,'bootstrap-input')
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
    else: raise ValueError('operation')
except Exception:
    sys.stderr.write('private deployment transfer preflight failed\n');sys.exit(1)
`

func prepareTarget(ctx context.Context, transport setupssh.Client, root string) error {
	if _, err := transport.Run(ctx, []string{"python3", "-c", targetFiles, "prepare", root}, nil); err != nil {
		return errors.New("target deployment directory preflight failed; private output omitted")
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

func uploadBytes(ctx context.Context, transport setupssh.Client, root string, raw []byte, remote string, mode fs.FileMode) error {
	return uploadReader(ctx, transport, root, remote, sha(raw), mode, bytes.NewReader(raw), int64(len(raw)))
}
