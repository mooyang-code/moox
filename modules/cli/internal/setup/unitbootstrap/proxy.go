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
	Origin  string `json:"origin"`
	Mode    string `json:"mode"`
	CA      string `json:"ca_sha256"`
}

func proxyOperation(ctx context.Context, binary, configFile, source, mode, operation string, importExisting bool, lock unitruntime.Options) (string, error) {
	configuration := map[string]any{"tls": map[string]string{
		"mode": mode, "storage_root": filepath.Join(source, "console-proxy/data/caddy/caddy"),
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
	if importExisting {
		if operation == "check-state" {
			args = append(args, "--for-import")
		} else {
			args = append(args, "--import-existing")
		}
	}
	raw, err = runOffline(ctx, exec.CommandContext(ctx, binary, args...), configFile, lock)
	if err != nil {
		return "", err
	}
	var metadata struct {
		CA string `json:"ca_sha256"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&metadata) != nil || decoder.Decode(new(any)) != io.EOF || !validProxyFingerprint(metadata.CA) || mode == "internal" && metadata.CA == "" {
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
		if input.request.ProxyCA.Create || input.request.ProxyCA.ImportDirectory != "" {
			return "", errors.New("proxy CA authorization requires a selected console-proxy")
		}
		return "", nil
	}
	candidate, err := fsutil.OpenPhysicalRoot(prepared.Directory, true)
	if err != nil {
		return "", err
	}
	raw, err := fsutil.ReadPrivate(candidate, "console-proxy/config/app.yaml", 64<<10)
	candidate.Close()
	if err != nil {
		return "", err
	}
	var fields struct {
		TLS struct {
			Mode string `yaml:"mode"`
		} `yaml:"tls"`
	}
	if yaml.Unmarshal(raw, &fields) != nil || fields.TLS.Mode != "internal" && fields.TLS.Mode != "public" {
		return "", errors.New("proxy checkpoint requires explicit internal or public TLS mode")
	}
	mode := fields.TLS.Mode
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
	operation := func(source, operation string, imported bool) (string, error) {
		return proxyOperation(ctx, binary, configFile, source, mode, operation, imported, lock)
	}
	material := filepath.Join(root.Name(), "material")
	var receipt proxyReceipt
	if _, err := root.Lstat("receipt.json"); os.IsNotExist(err) {
		// Never infer authorization from missing data or an older release. An
		// explicit closed legacy snapshot must carry its original trust proof.
		for _, name := range []string{"material", ".staging", ".publish"} {
			if _, err := root.Lstat(name); !os.IsNotExist(err) {
				return "", errors.New("proxy receipt is missing beside existing initialization material")
			}
		}
		authorization := input.request.ProxyCA
		receipt = proxyReceipt{Version: 1, HostID: j.HostID, Phase: "pending", Origin: "public", Mode: mode}
		if authorization.ImportDirectory != "" {
			receipt.Origin = "imported"
			receipt.CA, err = operation(authorization.ImportDirectory, "check-state", true)
			if err != nil || receipt.CA == "" {
				return "", errors.New("legacy import requires valid CA keys, certificate and original published trust proof")
			}
		} else if mode == "internal" {
			if !authorization.Create {
				return "", errors.New("first internal proxy install requires explicit one-time CA creation or trusted import")
			}
			if j.Control.Previous != "" {
				old, err := unitinstall.ReadInstalled(ctx, j.Control.Previous)
				if err != nil || slices.Contains(old.Components, "console-proxy") {
					return "", errors.New("existing proxy identity requires trusted import; creation is refused")
				}
			}
			receipt.Origin = "generated"
		} else if authorization.Create {
			return "", errors.New("public TLS does not authorize creating an unused internal CA")
		}
		if err := writeProxyReceipt(root, receipt, false); err != nil {
			return "", err
		}
	} else {
		raw, err := fsutil.ReadPrivate(root, "receipt.json", 4096)
		if err != nil || decode(raw, &receipt) != nil || !receipt.valid(j.HostID, mode) {
			return "", errors.New("proxy initialization receipt is invalid or belongs to another host")
		}
	}
	if receipt.Origin == "public" && mode == "internal" {
		return "", errors.New("changing public-only initialization to internal CA requires explicit identity rotation")
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
		if receipt.Origin == "imported" {
			if input.request.ProxyCA.ImportDirectory == "" {
				return "", errors.New("pending proxy import requires its explicitly authorized closed source")
			}
			if err := unitinstall.CopyOfflineState(ctx, input.request.ProxyCA.ImportDirectory, stage, proxyPaths); err != nil {
				return "", err
			}
		} else {
			for _, name := range proxyPaths {
				if err := root.MkdirAll(filepath.Join(".staging", name), 0o700); err != nil {
					return "", err
				}
			}
		}
		ca, err := operation(stage, "initialize-state", receipt.Origin == "imported")
		if err != nil || receipt.CA != "" && ca != receipt.CA {
			return "", errors.New("offline proxy initialization/import failed its authorized identity check")
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
	ca, err := operation(material, "check-state", false)
	if err != nil || receipt.CA != "" && ca != receipt.CA || receipt.Origin == "public" && ca != "" {
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
	// Upgrade from the latest closed runtime state, preserving ACME caches and
	// renewed intermediates, while comparing its CA with the durable anchor.
	if j.Control.Previous != "" {
		old, err := unitinstall.ReadInstalled(ctx, j.Control.Previous)
		if err != nil {
			return "", err
		}
		if slices.Contains(old.Components, "console-proxy") {
			oldCA, err := operation(old.Directory, "check-state", false)
			if err != nil || oldCA != receipt.CA {
				return "", errors.New("closed proxy release no longer matches its persistent CA identity")
			}
			return old.Directory, nil
		}
	}
	return material, nil
}

func (r proxyReceipt) valid(hostID, mode string) bool {
	if r.Version != 1 || r.HostID != hostID || !validProxyFingerprint(r.CA) || r.Phase != "pending" && r.Phase != "ready" || r.Mode != "internal" && r.Mode != "public" || r.Phase == "pending" && r.Mode != mode {
		return false
	}
	switch r.Origin {
	case "generated":
		return r.Mode == "internal" && (r.Phase == "pending" && r.CA == "" || r.Phase == "ready" && r.CA != "")
	case "imported":
		return r.CA != ""
	case "public":
		return r.Mode == "public" && r.CA == ""
	default:
		return false
	}
}

func writeProxyReceipt(root *os.Root, receipt proxyReceipt, replace bool) error {
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return fsutil.WritePrivate(root, "receipt.json", append(raw, '\n'), replace)
}
