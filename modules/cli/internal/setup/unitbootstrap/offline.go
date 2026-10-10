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
	raw, err := runOffline(ctx, command, master, lock)
	if err != nil {
		return metadata, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&metadata) != nil || decoder.Decode(new(any)) != io.EOF {
		return metadata, errors.New("offline Admin returned invalid public metadata")
	}
	return metadata, nil
}

func offlineEventBus(ctx context.Context, binary, database, master, hostID, output string, lock unitruntime.Options) error {
	for _, operation := range []string{"ensure", "export"} {
		args := []string{"eventbus-credentials", operation, "--db-path", database, "--encryption-key-file", master, "--node-id", hostID}
		if operation == "export" {
			args = append(args, "--output-dir", output)
		}
		raw, err := runOffline(ctx, exec.CommandContext(ctx, binary, args...), master, lock)
		if err != nil {
			return err
		}
		var metadata struct {
			Status    string   `json:"status"`
			Roles     []string `json:"roles"`
			TLS       bool     `json:"tls"`
			OutputDir string   `json:"output_dir"`
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&metadata) != nil || decoder.Decode(new(any)) != io.EOF || metadata.Status != "ok" || len(metadata.Roles) == 0 || operation == "export" && metadata.OutputDir != output || operation == "ensure" && !metadata.TLS {
			return errors.New("offline EventBus issuance returned invalid public metadata")
		}
	}
	return nil
}

func runOffline(ctx context.Context, command *exec.Cmd, master string, lock unitruntime.Options) ([]byte, error) {
	command.Dir = filepath.Dir(command.Path)
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + filepath.Dir(master)}
	var stdout boundedOutput
	command.Stdout, command.Stderr = &stdout, io.Discard
	closeLock, err := offlineProcess(command, lock)
	if err != nil {
		return nil, err
	}
	defer closeLock()
	// Linux parent-death signals track the creating OS thread. Keep that thread
	// alive for the entire child lifetime, including cancellation and Wait.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("offline Admin operation failed; private child output omitted")
	}
	return stdout.buffer.Bytes(), nil
}
