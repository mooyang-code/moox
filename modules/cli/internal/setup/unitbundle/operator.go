package unitbundle

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io/fs"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
	"gopkg.in/yaml.v3"
)

// OperatorPin is the public CA binding installed by the core bootstrap.
type OperatorPin struct {
	ControlHostID string `json:"control_host_id"`
	CA            string `json:"ca_sha256"`
}

func ReadOperatorPin(directory string) (OperatorPin, error) {
	var pin OperatorPin
	root, err := fsutil.OpenPhysicalRoot(directory, true)
	if err != nil {
		return pin, err
	}
	defer root.Close()
	raw, err := fsutil.ReadPrivate(root, "gateway-bootstrap.json", 4096)
	if err != nil {
		return pin, err
	}
	if err := canonical(raw, &pin); err != nil {
		return pin, err
	}
	if pin.ControlHostID == "" || !validHash(pin.CA) {
		return pin, errors.New("operator bootstrap pin is invalid")
	}
	return pin, nil
}

// FetchMetadata reads only the bounded public inventory through verified SSH.
func FetchMetadata(ctx context.Context, download Download, directory string, options Options) (hostbundle.Metadata, error) {
	var metadata hostbundle.Metadata
	if download == nil {
		return metadata, errors.New("host metadata requires a verified SSH downloader")
	}
	raw, err := downloadFile(ctx, download, path.Join(directory, "bundle.json"), maxMetadata)
	if err != nil {
		return metadata, err
	}
	if err := canonical(raw, &metadata); err != nil {
		return metadata, err
	}
	if metadata.BundleDir != directory {
		return metadata, errors.New("host metadata does not match its bootstrap directory")
	}
	return metadata, validateMetadata(metadata, options)
}

// InstallOperator downloads only the operator identity and public CA from a
// control bundle over verified SSH. Service and gateway private keys stay on
// the target. The configuration is switched only after its immutable key has
// been written durably, making an interrupted installation safe to repeat.
func InstallOperator(ctx context.Context, download Download, source hostbundle.Metadata, options Options, directory string) error {
	if download == nil || !options.AllowOperator {
		return errors.New("operator installation requires the verified control bootstrap channel")
	}
	if err := validateMetadata(source, options); err != nil {
		return err
	}
	raw, err := downloadFile(ctx, download, path.Join(source.BundleDir, "bundle.json"), maxMetadata)
	if err != nil {
		return err
	}
	var metadata hostbundle.Metadata
	if err := canonical(raw, &metadata); err != nil || !reflect.DeepEqual(metadata, source) {
		return errors.New("operator material differs from the pinned bootstrap metadata")
	}
	files := map[string][]byte{}
	for _, name := range []string{"operator/gateway-client.yaml", "operator/caller-moox-cli.key", "certs/moox-ca.crt"} {
		i := slices.IndexFunc(source.Files, func(file hostbundle.File) bool { return file.Path == name })
		if i < 0 {
			return errors.New("operator material is missing an inventoried file")
		}
		item := source.Files[i]
		content, err := downloadFile(ctx, download, path.Join(source.BundleDir, name), item.Size)
		if err != nil || int64(len(content)) != item.Size || digest(content) != item.SHA256 {
			return errors.New("operator material download does not match its inventory")
		}
		files[name] = content
	}
	ca, err := certificate(files["certs/moox-ca.crt"], source.CA.CertificateInfo)
	if err != nil || !ca.IsCA || !ca.BasicConstraintsValid || ca.KeyUsage&x509.KeyUsageCertSign == 0 || time.Now().Before(ca.NotBefore) || !time.Now().Before(ca.NotAfter) || ca.CheckSignatureFrom(ca) != nil {
		return errors.New("operator material has an invalid pinned CA")
	}
	i := slices.IndexFunc(source.Credentials, func(c hostbundle.Credential) bool { return c.Caller == "moox-cli" })
	if i < 0 {
		return errors.New("operator material has no assigned operator identity")
	}
	credential := source.Credentials[i]
	config := hostbundle.OperatorConfig{Caller: "moox-cli", KeyID: credential.KeyID, KeyFile: "caller-moox-cli.key"}
	expected, err := yaml.Marshal(config)
	if err != nil || !bytes.Equal(files["operator/gateway-client.yaml"], expected) {
		return errors.New("operator configuration does not match its assigned identity")
	}
	key := files[credential.KeyFile]
	if _, err := signingSecret(key); err != nil {
		return err
	}
	root, err := fsutil.OpenPhysicalRoot(directory, true)
	if err != nil {
		return err
	}
	defer root.Close()
	// A private pin prevents replacing an installed identity from another
	// deployment. Rotation belongs to the explicit credential workflow.
	pin, err := json.Marshal(struct {
		ControlHostID string `json:"control_host_id"`
		CA            string `json:"ca_sha256"`
	}{source.ControlHostID, source.CA.SHA256})
	if err != nil {
		return err
	}
	pin = append(pin, '\n')
	if _, err := root.Lstat("gateway-bootstrap.json"); err == nil {
		old, err := fsutil.ReadPrivate(root, "gateway-bootstrap.json", 4096)
		if err != nil || !bytes.Equal(old, pin) {
			return errors.New("installed operator identity belongs to another control CA")
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	} else if err := fsutil.WritePrivate(root, "gateway-bootstrap.json", pin, false); err != nil {
		return err
	}
	keyName := "caller-moox-cli-" + digest(key) + ".key"
	if _, err := root.Lstat(keyName); err == nil {
		old, err := fsutil.ReadPrivate(root, keyName, 4096)
		if err != nil || !bytes.Equal(old, key) {
			return errors.New("installed operator key differs from its immutable identity")
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	} else if err := fsutil.WritePrivate(root, keyName, key, false); err != nil {
		return err
	}
	config.KeyFile = filepath.Join(directory, keyName)
	encoded, err := yaml.Marshal(config)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return fsutil.WritePrivate(root, "gateway-client.yaml", encoded, true)
}
