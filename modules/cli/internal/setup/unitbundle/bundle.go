// Package unitbundle verifies and transfers issued host material. It never
// creates trust roots or reads the operator manifest.
package unitbundle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"gopkg.in/yaml.v3"
)

const maxMetadata = 128 << 10

type Options struct {
	HostID, ControlHostID, Address, PrivateAddress, ControlAddress string
	Components                                                     []string
	ExpectedCA, ExpectedHash                                       string
	AllowOperator                                                  bool
}

// Material retains private bytes without exporting them through JSON or fmt.
type Material struct {
	metadata  hostbundle.Metadata
	files     map[string][]byte
	directory string
}

func (*Material) String() string               { return "MooXHostMaterial{private payload omitted}" }
func (m *Material) GoString() string           { return m.String() }
func (*Material) MarshalJSON() ([]byte, error) { return []byte(`{"private_material":"omitted"}`), nil }
func (m *Material) Directory() string          { return m.directory }
func (m *Material) Metadata() hostbundle.Metadata {
	copy := m.metadata
	copy.Files = slices.Clone(copy.Files)
	copy.Credentials = slices.Clone(copy.Credentials)
	return copy
}

func digest(raw []byte) string { hash := sha256.Sum256(raw); return hex.EncodeToString(hash[:]) }
func validHash(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 32 && value == strings.ToLower(value)
}
func canonical(raw []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("host material requires one JSON document with known fields")
	}
	encoded, err := json.Marshal(out)
	if err != nil || !bytes.Equal(raw, append(encoded, '\n')) {
		return errors.New("host material requires canonical JSON encoding")
	}
	return nil
}
func privatePath(name string) bool {
	return fs.ValidPath(name) && name != "." && len(name) <= 512 && !strings.ContainsAny(name, "\\\x00\r\n")
}
func validKeyID(value string) bool {
	return value != "" && len(value) <= 128 && utf8.ValidString(value) && !strings.ContainsAny(value, "/\\") && !strings.ContainsFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}
func wanted(options Options) ([]string, error) {
	if !servicecatalog.ValidHostID(options.HostID) || !servicecatalog.ValidHostID(options.ControlHostID) || !servicecatalog.ValidHostAddress(options.Address) || !servicecatalog.ValidHostAddress(options.ControlAddress) || options.PrivateAddress != "" && !servicecatalog.ValidHostAddress(options.PrivateAddress) || !validHash(options.ExpectedCA) || !validHash(options.ExpectedHash) || options.AllowOperator && options.HostID != options.ControlHostID {
		return nil, errors.New("host material requires canonical topology and pinned CA/snapshot identities")
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return nil, err
	}
	callers := []string{"host-agent", "host-gateway@" + options.HostID}
	seen := map[string]bool{}
	for _, id := range options.Components {
		component, ok := catalog.Component(id)
		if !ok || seen[id] || component.Scope == servicecatalog.ScopeControl && options.HostID != options.ControlHostID {
			return nil, errors.New("host material components do not match a valid target placement")
		}
		seen[id] = true
		if id != "host-gateway" {
			callers = append(callers, id)
		}
		if id == "console-proxy" {
			callers = append(callers, "console")
		}
	}
	if options.AllowOperator {
		callers = append(callers, "moox-cli")
	}
	slices.Sort(callers)
	return slices.Compact(callers), nil
}

func validateMetadata(metadata hostbundle.Metadata, options Options) error {
	callers, err := wanted(options)
	if err != nil {
		return err
	}
	if metadata.Version != 1 || metadata.HostID != options.HostID || metadata.ControlHostID != options.ControlHostID || metadata.CA.SHA256 != options.ExpectedCA || metadata.ExpectedHash != options.ExpectedHash || metadata.ConfigFile != "host-gateway/config/app.yaml" || len(metadata.Credentials) != len(callers) || len(metadata.Files) == 0 || len(metadata.Files) > 256 || !path.IsAbs(metadata.BundleDir) || metadata.BundleDir == "/" || len(metadata.BundleDir) > 4096 || path.Clean(metadata.BundleDir) != metadata.BundleDir || strings.ContainsAny(metadata.BundleDir, "\\\x00\r\n") {
		return errors.New("host material metadata does not match its expected topology and identity")
	}
	previous := ""
	keyIDs := map[string]bool{}
	allowed := map[string]bool{metadata.ConfigFile: true, "certs/moox-ca.crt": true, "certs/host-gateway/server.crt": true, "certs/host-gateway/server.key": true}
	for _, credential := range metadata.Credentials {
		name := credential.Caller
		if name == "host-gateway@"+metadata.HostID {
			name = "host-gateway"
		}
		file := "secrets/caller-" + name + ".key"
		if name == "moox-cli" {
			file = "operator/caller-moox-cli.key"
		}
		if credential.Caller <= previous || !slices.Contains(callers, credential.Caller) || credential.KeyFile != file || !validKeyID(credential.KeyID) || keyIDs[credential.KeyID] || !privatePath(credential.KeyFile) {
			return errors.New("host material has an unrelated, repeated or misplaced credential")
		}
		keyIDs[credential.KeyID] = true
		allowed[credential.KeyFile] = true
		if name == "moox-cli" {
			allowed["operator/gateway-client.yaml"] = true
		}
		previous = credential.Caller
	}
	wantVerification := ""
	if slices.Contains(options.Components, "access") {
		wantVerification = "secrets/access/access-verification.json"
	}
	if metadata.VerificationFile != wantVerification {
		return errors.New("host material Access verification table does not match placement")
	}
	if wantVerification != "" {
		allowed[wantVerification] = true
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return err
	}
	previous = ""
	var total int64
	for _, file := range metadata.Files {
		total += file.Size
		if !privatePath(file.Path) || file.Path <= previous || file.Path == "bundle.json" || !validHash(file.SHA256) || file.Size <= 0 || file.Size > 1<<20 || total > 16<<20 {
			return errors.New("host material file inventory is invalid or exceeds limits")
		}
		if !allowed[file.Path] {
			external := wantVerification != "" && path.Dir(file.Path) == path.Dir(wantVerification) && strings.HasSuffix(file.Path, ".key") && slices.ContainsFunc(catalog.Principals, func(p servicecatalog.Principal) bool {
				return strings.HasPrefix(path.Base(file.Path), "caller-"+p.ID+"-")
			})
			if !external {
				return errors.New("host material inventory contains a file outside its target identities")
			}
		}
		previous = file.Path
	}
	return nil
}

func Load(ctx context.Context, directory string, options Options) (*Material, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := fsutil.OpenPhysicalRoot(directory, true)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	raw, err := fsutil.ReadPrivate(root, "bundle.json", maxMetadata)
	if err != nil {
		return nil, err
	}
	var metadata hostbundle.Metadata
	if err := canonical(raw, &metadata); err != nil {
		return nil, err
	}
	if err := validateMetadata(metadata, options); err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	expected := map[string]bool{"bundle.json": true}
	directories := map[string]bool{".": true}
	for _, file := range metadata.Files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		raw, err := fsutil.ReadPrivate(root, file.Path, file.Size)
		if err != nil {
			return nil, err
		}
		if int64(len(raw)) != file.Size || digest(raw) != file.SHA256 {
			return nil, errors.New("host material file digest mismatch")
		}
		files[file.Path] = raw
		expected[file.Path] = true
		for dir := path.Dir(file.Path); dir != "."; dir = path.Dir(dir) {
			directories[dir] = true
		}
	}
	if err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() && directories[name] && info.Mode().Perm() == 0o700 && fsutil.Owned(info) {
			return nil
		}
		if !entry.IsDir() && expected[name] && info.Mode().IsRegular() && info.Mode().Perm() == 0o600 && fsutil.Owned(info) {
			return nil
		}
		return errors.New("host material contains an undeclared file, link or unsafe directory")
	}); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	material, err := verify(metadata, files, options)
	if material != nil {
		material.directory = directory
	}
	return material, err
}

func verify(metadata hostbundle.Metadata, files map[string][]byte, options Options) (*Material, error) {
	allowed := map[string]bool{metadata.ConfigFile: true, "certs/moox-ca.crt": true, "certs/host-gateway/server.crt": true, "certs/host-gateway/server.key": true}
	credentials := []gatewayauth.Credentials{}
	gatewayKeyID := ""
	for _, credential := range metadata.Credentials {
		allowed[credential.KeyFile] = true
		secret, err := signingSecret(files[credential.KeyFile])
		if err != nil {
			return nil, err
		}
		credentials = append(credentials, gatewayauth.Credentials{Caller: credential.Caller, KeyID: credential.KeyID, Secret: secret})
		if credential.Caller == "host-gateway@"+options.HostID {
			gatewayKeyID = credential.KeyID
		}
		if credential.Caller == "moox-cli" {
			allowed["operator/gateway-client.yaml"] = true
			config, err := yaml.Marshal(hostbundle.OperatorConfig{Caller: "moox-cli", KeyID: credential.KeyID, KeyFile: "caller-moox-cli.key"})
			if err != nil || !bytes.Equal(config, files["operator/gateway-client.yaml"]) {
				return nil, errors.New("host material operator configuration differs from its assigned identity")
			}
		}
	}
	if metadata.VerificationFile != "" {
		access, err := verifyAccess(files, metadata.VerificationFile, allowed)
		if err != nil {
			return nil, err
		}
		credentials = append(credentials, access...)
	}
	if _, err := gatewayauth.NewCredentialRegistry(credentials); err != nil {
		return nil, errors.New("host material credential registry is invalid")
	}
	if len(files) != len(allowed) {
		return nil, errors.New("host material contains missing or unrelated payload files")
	}
	for name := range files {
		if !allowed[name] {
			return nil, errors.New("host material contains a payload outside its target identities")
		}
	}
	config, err := hostgatewayconfig.Decode(bytes.NewReader(files[metadata.ConfigFile]))
	if err != nil {
		return nil, errors.New("host material gateway configuration is invalid")
	}
	if config != hostgatewayconfig.Default(options.HostID, options.ControlHostID, options.ControlAddress, gatewayKeyID) {
		return nil, errors.New("host material gateway configuration differs from target topology")
	}
	if err := verifyCertificates(metadata, files, options); err != nil {
		return nil, err
	}
	return &Material{metadata: metadata, files: files}, nil
}
