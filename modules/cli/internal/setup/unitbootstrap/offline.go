package unitbootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
)

type boundedOutput struct{ buffer bytes.Buffer }

func (b *boundedOutput) Write(raw []byte) (int, error) {
	if len(raw) > 128<<10-b.buffer.Len() {
		return 0, errors.New("offline Admin output exceeds public metadata limit")
	}
	return b.buffer.Write(raw)
}

func offlineAdmin(ctx context.Context, binary, topology, database, master, pki, output string, lock unitruntime.Options) (hostbundle.Metadata, error) {
	var metadata hostbundle.Metadata
	command := exec.CommandContext(ctx, binary, "bootstrap", "--topology-file", topology, "--db-path", database, "--encryption-key-file", master, "--pki-dir", pki, "--output-dir", output)
	command.Dir = filepath.Dir(binary)
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + filepath.Dir(master)}
	var stdout boundedOutput
	command.Stdout, command.Stderr = &stdout, io.Discard
	closeLock, err := offlineProcess(command, lock)
	if err != nil {
		return metadata, err
	}
	defer closeLock()
	// Linux parent-death signals track the creating OS thread. Keep that thread
	// alive for the entire child lifetime, including cancellation and Wait.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return metadata, ctx.Err()
		}
		return metadata, errors.New("offline Admin initialization failed; private child output omitted")
	}
	decoder := json.NewDecoder(bytes.NewReader(stdout.buffer.Bytes()))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&metadata) != nil || decoder.Decode(new(any)) != io.EOF {
		return metadata, errors.New("offline Admin returned invalid public metadata")
	}
	return metadata, nil
}
