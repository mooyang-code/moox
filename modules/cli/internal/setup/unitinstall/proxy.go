package unitinstall

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
)

func validateProxyPaths(prepared Prepared, upgrading bool) error {
	root, err := fsutil.OpenPhysicalRoot(prepared.Directory, true)
	if err != nil {
		return err
	}
	defer root.Close()
	raw, err := fsutil.ReadPrivate(root, "console-proxy/config/app.yaml", 1<<20)
	if err != nil {
		return err
	}
	document, err := yamlDocument(raw)
	if err != nil {
		return err
	}
	var fields struct {
		TLS struct {
			StorageRoot  string `yaml:"storage_root"`
			CABaseline   string `yaml:"ca_baseline"`
			CAPublishDir string `yaml:"ca_publish_dir"`
			InitializeCA bool   `yaml:"initialize_ca"`
		} `yaml:"tls"`
	}
	if document.Decode(&fields) != nil {
		return errors.New("proxy state configuration is invalid")
	}
	if upgrading && fields.TLS.InitializeCA {
		return errors.New("proxy upgrades must disable first-install CA initialization")
	}
	for _, bound := range []struct{ value, directory string }{{fields.TLS.StorageRoot, "data"}, {fields.TLS.CABaseline, "data"}, {fields.TLS.CAPublishDir, "certs"}} {
		if bound.value == "" {
			return errors.New("proxy state paths must be explicit")
		}
		resolved := bound.value
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(prepared.Directory, "console-proxy/config", resolved)
		}
		base := filepath.Join(prepared.Directory, "console-proxy", bound.directory)
		if filepath.Clean(resolved) != base && !strings.HasPrefix(filepath.Clean(resolved), base+string(filepath.Separator)) {
			return errors.New("proxy state paths must stay inside their copied component state")
		}
	}
	return nil
}

type boundedOutput struct{ buffer bytes.Buffer }

func (b *boundedOutput) Write(raw []byte) (int, error) {
	if len(raw) > 4096-b.buffer.Len() {
		return 0, errors.New("proxy state output exceeds its bound")
	}
	return b.buffer.Write(raw)
}

func (b *boundedOutput) Bytes() []byte { return b.buffer.Bytes() }

func proxyState(ctx context.Context, prepared Prepared) (string, error) {
	if err := validateProxyPaths(prepared, false); err != nil {
		return "", err
	}
	command := exec.CommandContext(ctx, filepath.Join(prepared.Directory, "bin/moox-console-proxy"), "check-state", "--config", filepath.Join(prepared.Directory, "console-proxy/config/app.yaml"))
	command.Dir = filepath.Join(prepared.Directory, "console-proxy")
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + command.Dir}
	var output boundedOutput
	command.Stdout, command.Stderr = &output, io.Discard
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("persisted proxy CA state failed validation")
	}
	var result struct {
		CA string `json:"ca_sha256"`
	}
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || decoder.Decode(new(any)) != io.EOF {
		return "", errors.New("proxy state checker returned invalid public metadata")
	}
	if result.CA != "" {
		digest, err := hex.DecodeString(strings.ReplaceAll(result.CA, ":", ""))
		if err != nil || len(digest) != 32 {
			return "", errors.New("proxy state checker returned an invalid CA fingerprint")
		}
	}
	return result.CA, nil
}
