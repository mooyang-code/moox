package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mooyang-code/moox/modules/admin/internal/pki"
	"github.com/mooyang-code/moox/modules/admin/internal/privatefiles"
	"github.com/mooyang-code/moox/modules/admin/internal/service/keys"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/security"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"gopkg.in/yaml.v3"
)

type hostBundleCredential struct {
	Caller  string `json:"caller"`
	KeyID   string `json:"key_id"`
	KeyFile string `json:"key_file"`
}

type hostBundleFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type hostBundleResult struct {
	Version          int                    `json:"version"`
	HostID           string                 `json:"host_id"`
	ControlHostID    string                 `json:"control_host_id"`
	BundleDir        string                 `json:"bundle_dir"`
	ConfigFile       string                 `json:"config_file"`
	VerificationFile string                 `json:"verification_file,omitempty"`
	ExpectedHash     string                 `json:"expected_hash"`
	CA               pki.CAInfo             `json:"ca"`
	Certificate      pki.CertificateInfo    `json:"certificate"`
	Credentials      []hostBundleCredential `json:"credentials"`
	Files            []hostBundleFile       `json:"files"`
}

type hostBundleMaterial struct {
	host, control bootstrapHost
	signing       []keys.SigningKey
	access        []keys.VerificationKey
	caInfo        pki.CAInfo
	expectedHash  string
	operator      bool
}

func hostSigningCallers(host bootstrapHost, operator bool) []string {
	callers := append([]string{"host-agent", "host-gateway@" + host.HostID}, host.Components...)
	if slices.Contains(host.Components, "console-proxy") {
		callers = append(callers, "console")
	}
	if operator {
		callers = append(callers, "moox-cli")
	}
	callers = slices.DeleteFunc(callers, func(caller string) bool { return caller == "host-gateway" })
	slices.Sort(callers)
	return slices.Compact(callers)
}

// publishHostBundle is shared by offline bootstrap and normal deployments.
// A fresh private directory is the publication boundary; no partial directory
// is advertised and neither the Admin master key nor CA private key is copied.
func publishHostBundle(parentDir string, material hostBundleMaterial, ca *pki.Store) (hostBundleResult, error) {
	parent, err := privatefiles.OpenRoot(parentDir)
	if err != nil {
		return hostBundleResult{}, err
	}
	defer parent.Close()
	id, err := security.RandomHex(16)
	if err != nil {
		return hostBundleResult{}, err
	}
	stageName, finalName := ".host-bundle-"+id, "host-bundle-"+id
	if err := parent.Mkdir(stageName, 0o700); err != nil {
		return hostBundleResult{}, err
	}
	defer parent.RemoveAll(stageName)
	stage, err := parent.OpenRoot(stageName)
	if err != nil {
		return hostBundleResult{}, err
	}
	defer stage.Close()
	stageDir := filepath.Join(parentDir, stageName)
	result := hostBundleResult{
		Version: 1, HostID: material.host.HostID, ControlHostID: material.control.HostID,
		BundleDir: filepath.Join(parentDir, finalName), ConfigFile: "host-gateway/config/app.yaml",
		CA: material.caInfo, ExpectedHash: material.expectedHash,
	}
	wanted := hostSigningCallers(material.host, material.operator)
	var gatewayKeyID string
	for _, key := range material.signing {
		if !slices.Contains(wanted, key.Caller) || slices.ContainsFunc(result.Credentials, func(c hostBundleCredential) bool { return c.Caller == key.Caller }) {
			return hostBundleResult{}, errors.New("host bundle contains an unrelated or duplicate signing identity")
		}
		caller, directory := key.Caller, "secrets"
		if caller == "moox-cli" {
			directory = "operator"
		}
		if caller == "host-gateway@"+material.host.HostID {
			caller, gatewayKeyID = "host-gateway", key.KeyID
		}
		root, err := privatefiles.OpenSubRoot(stage, directory)
		if err != nil {
			return hostBundleResult{}, err
		}
		name := "caller-" + caller + ".key"
		err = privatefiles.Write(root, name, []byte(key.Credentials().Secret+"\n"))
		if err == nil && key.Caller == "moox-cli" {
			var config []byte
			config, err = yaml.Marshal(gatewayclient.FileConfig{Caller: "moox-cli", KeyID: key.KeyID, KeyFile: name})
			if err == nil {
				err = privatefiles.Write(root, "gateway-client.yaml", config)
			}
		}
		root.Close()
		if err != nil {
			return hostBundleResult{}, err
		}
		result.Credentials = append(result.Credentials, hostBundleCredential{Caller: key.Caller, KeyID: key.KeyID, KeyFile: filepath.ToSlash(filepath.Join(directory, name))})
	}
	if gatewayKeyID == "" || len(result.Credentials) != len(wanted) {
		return hostBundleResult{}, errors.New("host bundle signing identities are incomplete")
	}
	slices.SortFunc(result.Credentials, func(a, b hostBundleCredential) int {
		return strings.Compare(a.Caller, b.Caller)
	})
	if slices.Contains(material.host.Components, "access") {
		if len(material.access) == 0 {
			return hostBundleResult{}, errors.New("Access requires external verification keys")
		}
		if err := writeAccessKeys(material.access, filepath.Join(stageDir, "secrets", "access")); err != nil {
			return hostBundleResult{}, err
		}
		result.VerificationFile = "secrets/access/access-verification.json"
	}
	config, err := hostgatewayconfig.Encode(hostgatewayconfig.Default(material.host.HostID, material.control.HostID, material.control.Address, gatewayKeyID))
	if err != nil {
		return hostBundleResult{}, err
	}
	gateway, err := privatefiles.OpenSubRoot(stage, "host-gateway")
	if err != nil {
		return hostBundleResult{}, err
	}
	defer gateway.Close()
	configRoot, err := privatefiles.OpenSubRoot(gateway, "config")
	if err != nil {
		return hostBundleResult{}, err
	}
	defer configRoot.Close()
	if err := privatefiles.Write(configRoot, "app.yaml", config); err != nil {
		return hostBundleResult{}, err
	}
	certs, err := privatefiles.OpenSubRoot(stage, "certs")
	if err != nil {
		return hostBundleResult{}, err
	}
	defer certs.Close()
	issued, err := ca.Issue(pki.HostIdentity{HostID: material.host.HostID, Address: material.host.Address, PrivateAddress: material.host.PrivateAddress}, filepath.Join(stageDir, "certs", "host-gateway"))
	if err != nil {
		return hostBundleResult{}, err
	}
	if issued.CAFingerprint != material.caInfo.SHA256 {
		return hostBundleResult{}, errors.New("MooX CA changed while issuing the host certificate")
	}
	result.Certificate = issued.CertificateInfo
	if err := certs.Rename("host-gateway/moox-ca.crt", "moox-ca.crt"); err != nil {
		return hostBundleResult{}, err
	}
	result.Files, err = hostBundleInventory(stage)
	if err != nil {
		return hostBundleResult{}, err
	}
	// Public identities, paths and hashes only. The separate operator directory
	// is present only during control bootstrap and must not become a service key.
	metadata, err := json.Marshal(result)
	if err != nil {
		return hostBundleResult{}, err
	}
	if err := privatefiles.Write(stage, "bundle.json", append(metadata, '\n')); err != nil {
		return hostBundleResult{}, err
	}
	for _, root := range []*os.Root{configRoot, gateway, certs, stage} {
		if err := root.Close(); err != nil {
			return hostBundleResult{}, err
		}
	}
	if err := parent.Rename(stageName, finalName); err != nil {
		return hostBundleResult{}, err
	}
	if err := privatefiles.SyncDirectory(parent); err != nil {
		return hostBundleResult{}, err
	}
	return result, nil
}

func hostBundleInventory(root *os.Root) ([]hostBundleFile, error) {
	var files []hostBundleFile
	var total int64
	err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() && info.Mode().Perm() == 0o700 {
			return nil
		}
		total += info.Size()
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() <= 0 || info.Size() > 1<<20 || total > 16<<20 || len(files) >= 256 {
			return errors.New("host bundle contains an invalid private file")
		}
		raw, err := root.ReadFile(name)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(raw)
		files = append(files, hostBundleFile{Path: name, SHA256: hex.EncodeToString(digest[:]), Size: int64(len(raw))})
		return nil
	})
	return files, err
}
