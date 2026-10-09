package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/mooyang-code/moox/modules/admin/internal/pki"
	"github.com/mooyang-code/moox/modules/admin/internal/privatefiles"
	"github.com/mooyang-code/moox/modules/admin/internal/service/keys"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/security"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"gopkg.in/yaml.v3"
)

type bootstrapCredential struct {
	Caller  string `json:"caller"`
	KeyID   string `json:"key_id"`
	KeyFile string `json:"key_file"`
}

type bootstrapResult struct {
	Version       int                   `json:"version"`
	ControlHostID string                `json:"control_host_id"`
	BundleDir     string                `json:"bundle_dir"`
	ConfigFile    string                `json:"config_file"`
	ExpectedHash  string                `json:"expected_hash"`
	CA            pki.CAInfo            `json:"ca"`
	Credentials   []bootstrapCredential `json:"credentials"`
}

func publishBootstrapBundle(parentDir string, control bootstrapHost, exported []keys.SigningKey, ca *pki.Store, info pki.CAInfo, hash string) (bootstrapResult, error) {
	parent, err := privatefiles.OpenRoot(parentDir)
	if err != nil {
		return bootstrapResult{}, err
	}
	defer parent.Close()
	id, err := security.RandomHex(16)
	if err != nil {
		return bootstrapResult{}, err
	}
	stageName, finalName := ".bootstrap-"+id, "bootstrap-"+id
	if err := parent.Mkdir(stageName, 0o700); err != nil {
		return bootstrapResult{}, err
	}
	defer parent.RemoveAll(stageName)
	stage, err := parent.OpenRoot(stageName)
	if err != nil {
		return bootstrapResult{}, err
	}
	defer stage.Close()
	stageDir := filepath.Join(parentDir, stageName)
	result := bootstrapResult{Version: 1, ControlHostID: control.HostID, BundleDir: filepath.Join(parentDir, finalName), ConfigFile: "hostgateway/config/app.yaml", CA: info, ExpectedHash: hash}
	var gatewayKeyID string
	for _, key := range exported {
		caller, directory := key.Caller, "secrets"
		if caller == "moox-cli" {
			directory = "operator"
		}
		if caller == "host-gateway@"+control.HostID {
			caller, gatewayKeyID = "host-gateway", key.KeyID
		}
		root, err := privatefiles.OpenSubRoot(stage, directory)
		if err != nil {
			return bootstrapResult{}, err
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
			return bootstrapResult{}, err
		}
		result.Credentials = append(result.Credentials, bootstrapCredential{Caller: key.Caller, KeyID: key.KeyID, KeyFile: filepath.ToSlash(filepath.Join(directory, name))})
	}
	if gatewayKeyID == "" {
		return bootstrapResult{}, errors.New("bootstrap host signing key was not provisioned")
	}
	config, err := hostgatewayconfig.Encode(hostgatewayconfig.Default(control.HostID, control.HostID, control.Address, gatewayKeyID))
	if err != nil {
		return bootstrapResult{}, err
	}
	gateway, err := privatefiles.OpenSubRoot(stage, "hostgateway")
	if err != nil {
		return bootstrapResult{}, err
	}
	defer gateway.Close()
	configRoot, err := privatefiles.OpenSubRoot(gateway, "config")
	if err != nil {
		return bootstrapResult{}, err
	}
	defer configRoot.Close()
	if err := privatefiles.Write(configRoot, "app.yaml", config); err != nil {
		return bootstrapResult{}, err
	}
	certs, err := privatefiles.OpenSubRoot(stage, "certs")
	if err != nil {
		return bootstrapResult{}, err
	}
	defer certs.Close()
	issued, err := ca.Issue(pki.HostIdentity{HostID: control.HostID, Address: control.Address, PrivateAddress: control.PrivateAddress}, filepath.Join(stageDir, "certs", "host-gateway"))
	if err != nil {
		return bootstrapResult{}, err
	}
	if issued.CAFingerprint != info.SHA256 {
		return bootstrapResult{}, errors.New("MooX CA changed while issuing the bootstrap certificate")
	}
	if err := certs.Rename("host-gateway/moox-ca.crt", "moox-ca.crt"); err != nil {
		return bootstrapResult{}, err
	}
	// Metadata contains only identities, KeyIDs and relative paths. The operator
	// credential is separate so the deployment CLI can fetch it without installing
	// it into a service's signing configuration.
	metadata, err := json.Marshal(result)
	if err != nil {
		return bootstrapResult{}, err
	}
	if err := privatefiles.Write(stage, "bootstrap.json", append(metadata, '\n')); err != nil {
		return bootstrapResult{}, err
	}
	for _, root := range []*os.Root{configRoot, gateway, certs, stage} {
		if err := root.Close(); err != nil {
			return bootstrapResult{}, err
		}
	}
	if err := parent.Rename(stageName, finalName); err != nil {
		return bootstrapResult{}, err
	}
	if err := privatefiles.SyncDirectory(parent); err != nil {
		return bootstrapResult{}, err
	}
	return result, nil
}
