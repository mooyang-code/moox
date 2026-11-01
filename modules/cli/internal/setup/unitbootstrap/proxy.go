package unitbootstrap

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitinstall"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
	"gopkg.in/yaml.v3"
)

var proxyPaths = []string{"console-proxy/certs", "console-proxy/data"}

type proxyReceipt struct {
	Version int    `json:"version"`
	HostID  string `json:"host_id"`
	Phase   string `json:"phase"`
	CA      string `json:"ca_sha256"`
}

func proxyOperation(ctx context.Context, binary, configFile, source, operation string, lock unitruntime.Options) (string, error) {
	configuration := map[string]any{"tls": map[string]string{
		"storage_root":   filepath.Join(source, "console-proxy/data/caddy/caddy"),
		"ca_baseline":    filepath.Join(source, "console-proxy/data/caddy/internal-ca.sha256"),
		"ca_publish_dir": filepath.Join(source, "console-proxy/certs/caddy"),
	}}
	raw, err := yaml.Marshal(configuration)
	if err != nil {
		return "", err
	}
	parent, err := fsutil.OpenPhysicalRoot(filepath.Dir(configFile), true)
	if err != nil {
		return "", err
	}
	err = fsutil.WritePrivate(parent, filepath.Base(configFile), raw, true)
	parent.Close()
	if err != nil {
		return "", err
	}
	args := []string{operation, "--config", configFile}
	raw, err = runOffline(ctx, exec.CommandContext(ctx, binary, args...), configFile, lock)
	if err != nil {
		return "", err
	}
	var metadata struct {
		CA string `json:"ca_sha256"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&metadata) != nil || decoder.Decode(new(any)) != io.EOF || !validProxyFingerprint(metadata.CA) || metadata.CA == "" {
		return "", errors.New("offline proxy returned invalid public CA metadata")
	}
	return metadata.CA, nil
}

func validProxyFingerprint(value string) bool {
	if value == "" {
		return true
	}
	for i := range value {
		if (i%3 == 2) != (value[i] == ':') {
			return false
		}
	}
	raw, err := hex.DecodeString(strings.ReplaceAll(value, ":", ""))
	return err == nil && len(raw) == 32 && len(value) == 95 && strings.ToUpper(value) == value
}

// proxyCheckpoint runs only while the host maintenance lock is held and every
// old control writer is stopped. Receipt and material are outside release/data
// snapshots: deleting a candidate never authorizes creating a replacement CA.
func proxyCheckpoint(ctx context.Context, input inputs, j *journal, prepared unitinstall.Prepared, attempt *os.Root, lock unitruntime.Options) (string, error) {
	if !slices.Contains(prepared.Components, "console-proxy") {
		return "", nil
	}
	identity, err := fsutil.OpenPhysicalRoot(filepath.Join(input.request.DeploymentRoot, "identity"), true)
	if err != nil {
		return "", err
	}
	defer identity.Close()
	if err := privateDirectory(identity, "console-proxy"); err != nil {
		return "", err
	}
	root, err := fsutil.OpenPhysicalRoot(filepath.Join(identity.Name(), "console-proxy"), true)
	if err != nil {
		return "", err
	}
	defer root.Close()
	binary := filepath.Join(prepared.Directory, "bin/moox-console-proxy")
	configFile := filepath.Join(attempt.Name(), "proxy-state.yaml")
	operation := func(source, operation string) (string, error) {
		return proxyOperation(ctx, binary, configFile, source, operation, lock)
	}
	material := filepath.Join(root.Name(), "material")
	var receipt proxyReceipt
	if _, err := root.Lstat("receipt.json"); os.IsNotExist(err) {
		for _, name := range []string{"material", ".staging", ".publish"} {
			if _, err := root.Lstat(name); !os.IsNotExist(err) {
				return "", errors.New("proxy receipt is missing beside existing initialization material")
			}
		}
		// 首次安装自动创建 CA；上一版已有 Console Proxy 却没有回执时，绝不换掉原来的 CA。
		if j.Control.Previous != "" {
			old, err := unitinstall.ReadInstalled(ctx, j.Control.Previous)
			if err != nil || slices.Contains(old.Components, "console-proxy") {
				return "", errors.New("an existing proxy identity is never replaced; creation is refused")
			}
		}
		receipt = proxyReceipt{Version: 1, HostID: j.HostID, Phase: "pending"}
		if err := writeProxyReceipt(root, receipt, false); err != nil {
			return "", err
		}
	} else {
		raw, err := fsutil.ReadPrivate(root, "receipt.json", 4096)
		if err != nil || decode(raw, &receipt) != nil || !receipt.valid(j.HostID) {
			return "", errors.New("proxy initialization receipt is invalid or belongs to another host")
		}
	}
	if _, err := root.Lstat("material"); os.IsNotExist(err) && receipt.Phase == "pending" {
		// These stages have never been published or served. A killed offline
		// generator can be retried; published material is never regenerated.
		for _, name := range []string{".staging", ".publish"} {
			if err := root.RemoveAll(name); err != nil {
				return "", err
			}
			if err := root.Mkdir(name, 0o700); err != nil {
				return "", err
			}
		}
		stage := filepath.Join(root.Name(), ".staging")
		for _, name := range proxyPaths {
			if err := root.MkdirAll(filepath.Join(".staging", name), 0o700); err != nil {
				return "", err
			}
		}
		ca, err := operation(stage, "initialize-state")
		if err != nil || receipt.CA != "" && ca != receipt.CA {
			return "", errors.New("offline proxy initialization failed its authorized identity check")
		}
		publication := filepath.Join(root.Name(), ".publish")
		if err := unitinstall.CopyOfflineState(ctx, stage, publication, proxyPaths); err != nil {
			return "", err
		}
		publishRoot, err := fsutil.OpenPhysicalRoot(publication, true)
		if err != nil {
			return "", err
		}
		err = fsutil.SyncPrivateTree(ctx, publishRoot)
		publishRoot.Close()
		if err != nil {
			return "", err
		}
		parent, err := root.Open(".")
		if err != nil {
			return "", err
		}
		err = fsutil.RenameExclusive(parent, ".publish", "material")
		if err == nil {
			err = parent.Sync()
		}
		parent.Close()
		if err != nil {
			return "", err
		}
	}
	materialRoot, err := fsutil.OpenPhysicalRoot(material, true)
	if err != nil {
		return "", errors.New("consumed proxy CA material is missing; automatic regeneration is refused")
	}
	materialRoot.Close()
	ca, err := operation(material, "check-state")
	if err != nil || receipt.CA != "" && ca != receipt.CA {
		return "", errors.New("consumed proxy CA material is missing or changed; automatic regeneration is refused")
	}
	if receipt.Phase == "pending" {
		receipt.Phase, receipt.CA = "ready", ca
		if err := writeProxyReceipt(root, receipt, true); err != nil {
			return "", err
		}
	}
	for _, name := range []string{".staging", ".publish"} {
		if err := root.RemoveAll(name); err != nil {
			return "", err
		}
	}
	// Upgrade from the latest closed runtime state, preserving renewed
	// intermediates, while comparing its CA with the durable anchor.
	if j.Control.Previous != "" {
		old, err := unitinstall.ReadInstalled(ctx, j.Control.Previous)
		if err != nil {
			return "", err
		}
		if slices.Contains(old.Components, "console-proxy") {
			oldCA, err := operation(old.Directory, "check-state")
			if err != nil || oldCA != receipt.CA {
				return "", errors.New("closed proxy release no longer matches its persistent CA identity")
			}
			return old.Directory, nil
		}
	}
	return material, nil
}

func (r proxyReceipt) valid(hostID string) bool {
	if r.Version != 1 || r.HostID != hostID || !validProxyFingerprint(r.CA) {
		return false
	}
	return r.Phase == "pending" && r.CA == "" || r.Phase == "ready" && r.CA != ""
}

func writeProxyReceipt(root *os.Root, receipt proxyReceipt, replace bool) error {
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return fsutil.WritePrivate(root, "receipt.json", append(raw, '\n'), replace)
}
