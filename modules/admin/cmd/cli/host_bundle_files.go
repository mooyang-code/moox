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
	"github.com/mooyang-code/moox/packages/security"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"gopkg.in/yaml.v3"
)

type hostBundleMaterial struct {
	host, control hostbundle.Host
	signing       []keys.SigningKey
	access        []keys.VerificationKey
	caInfo        hostbundle.CAInfo
	expectedHash  string
	operator      bool
}

func hostSigningCallers(host hostbundle.Host, operator bool) []string {
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
func publishHostBundle(parentDir string, material hostBundleMaterial, ca *pki.Store) (hostbundle.Metadata, error) {
	parent, err := privatefiles.OpenRoot(parentDir)
	if err != nil {
		return hostbundle.Metadata{}, err
	}
	defer parent.Close()
	id, err := security.RandomHex(16)
	if err != nil {
		return hostbundle.Metadata{}, err
	}
	stageName, finalName := ".host-bundle-"+id, "host-bundle-"+id
	if err := parent.Mkdir(stageName, 0o700); err != nil {
		return hostbundle.Metadata{}, err
	}
	defer parent.RemoveAll(stageName)
	stage, err := parent.OpenRoot(stageName)
	if err != nil {
		return hostbundle.Metadata{}, err
	}
	defer stage.Close()
	stageDir := filepath.Join(parentDir, stageName)
	result := hostbundle.Metadata{
		Version: 1, HostID: material.host.HostID, ControlHostID: material.control.HostID,
		BundleDir: filepath.Join(parentDir, finalName), ConfigFile: "host-gateway/config/app.yaml",
		CA: material.caInfo, ExpectedHash: material.expectedHash,
	}
	wanted := hostSigningCallers(material.host, material.operator)
	var gatewayKeyID string
	for _, key := range material.signing {
		if !slices.Contains(wanted, key.Caller) || slices.ContainsFunc(result.Credentials, func(c hostbundle.Credential) bool { return c.Caller == key.Caller }) {
			return hostbundle.Metadata{}, errors.New("host bundle contains an unrelated or duplicate signing identity")
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
			return hostbundle.Metadata{}, err
		}
		name := "caller-" + caller + ".key"
		err = privatefiles.Write(root, name, []byte(key.Credentials().Secret+"\n"))
		if err == nil && key.Caller == "moox-cli" {
			var config []byte
			config, err = yaml.Marshal(hostbundle.OperatorConfig{Caller: "moox-cli", KeyID: key.KeyID, KeyFile: name})
			if err == nil {
				err = privatefiles.Write(root, "gateway-client.yaml", config)
			}
		}
		root.Close()
		if err != nil {
			return hostbundle.Metadata{}, err
		}
		result.Credentials = append(result.Credentials, hostbundle.Credential{Caller: key.Caller, KeyID: key.KeyID, KeyFile: filepath.ToSlash(filepath.Join(directory, name))})
	}
	if gatewayKeyID == "" || len(result.Credentials) != len(wanted) {
		return hostbundle.Metadata{}, errors.New("host bundle signing identities are incomplete")
	}
	slices.SortFunc(result.Credentials, func(a, b hostbundle.Credential) int {
		return strings.Compare(a.Caller, b.Caller)
	})
	if slices.Contains(material.host.Components, "access") {
		if len(material.access) == 0 {
			return hostbundle.Metadata{}, errors.New("Access requires external verification keys")
		}
		if err := writeAccessKeys(material.access, filepath.Join(stageDir, "secrets", "access")); err != nil {
			return hostbundle.Metadata{}, err
		}
		result.VerificationFile = "secrets/access/access-verification.json"
	}
	config, err := hostgatewayconfig.Encode(hostgatewayconfig.Default(material.host.HostID, material.control.HostID, material.control.Address, gatewayKeyID))
	if err != nil {
		return hostbundle.Metadata{}, err
	}
	gateway, err := privatefiles.OpenSubRoot(stage, "host-gateway")
	if err != nil {
		return hostbundle.Metadata{}, err
	}
	defer gateway.Close()
	configRoot, err := privatefiles.OpenSubRoot(gateway, "config")
	if err != nil {
		return hostbundle.Metadata{}, err
	}
	defer configRoot.Close()
	if err := privatefiles.Write(configRoot, "app.yaml", config); err != nil {
		return hostbundle.Metadata{}, err
	}
	certs, err := privatefiles.OpenSubRoot(stage, "certs")
	if err != nil {
		return hostbundle.Metadata{}, err
	}
	defer certs.Close()
	issued, err := ca.Issue(pki.HostIdentity{HostID: material.host.HostID, Address: material.host.Address, PrivateAddress: material.host.PrivateAddress}, filepath.Join(stageDir, "certs", "host-gateway"))
	if err != nil {
		return hostbundle.Metadata{}, err
	}
	if issued.CAFingerprint != material.caInfo.SHA256 {
		return hostbundle.Metadata{}, errors.New("MooX CA changed while issuing the host certificate")
	}
	result.Certificate = issued.CertificateInfo
	if err := certs.Rename("host-gateway/moox-ca.crt", "moox-ca.crt"); err != nil {
		return hostbundle.Metadata{}, err
	}
	result.Files, err = hostBundleInventory(stage)
	if err != nil {
		return hostbundle.Metadata{}, err
	}
	// Public identities, paths and hashes only. The separate operator directory
	// is present only during control bootstrap and must not become a service key.
	metadata, err := json.Marshal(result)
	if err != nil {
		return hostbundle.Metadata{}, err
	}
	if err := privatefiles.Write(stage, "bundle.json", append(metadata, '\n')); err != nil {
		return hostbundle.Metadata{}, err
	}
	for _, root := range []*os.Root{configRoot, gateway, certs, stage} {
		if err := root.Close(); err != nil {
			return hostbundle.Metadata{}, err
		}
	}
	if err := parent.Rename(stageName, finalName); err != nil {
		return hostbundle.Metadata{}, err
	}
	if err := privatefiles.SyncDirectory(parent); err != nil {
		return hostbundle.Metadata{}, err
	}
	return result, nil
}

func hostBundleInventory(root *os.Root) ([]hostbundle.File, error) {
	var files []hostbundle.File
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
		files = append(files, hostbundle.File{Path: name, SHA256: hex.EncodeToString(digest[:]), Size: int64(len(raw))})
		return nil
	})
	return files, err
}
