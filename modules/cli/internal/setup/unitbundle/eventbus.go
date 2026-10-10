package unitbundle

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
)

// FetchEventBusClients relays only the inventoried client roles and public CA.
// Broker ACLs, server keys and CA signing keys are never accepted.
func FetchEventBusClients(ctx context.Context, download Download, metadata hostbundle.ClientMetadata, roles []string, destination string) error {
	if download == nil {
		return errors.New("EventBus clients require verified SSH")
	}
	files, err := readEventBusClients(ctx, metadata, roles, func(name string, max int64) ([]byte, error) {
		return downloadFile(ctx, download, path.Join(metadata.OutputDir, name), max)
	})
	if err != nil {
		return err
	}
	_, err = fsutil.PublishPrivate(ctx, destination, files)
	return err
}

func LoadEventBusClients(ctx context.Context, metadata hostbundle.ClientMetadata, roles []string) error {
	return LoadEventBusClientsAt(ctx, metadata, roles, metadata.OutputDir)
}

func LoadEventBusClientsAt(ctx context.Context, metadata hostbundle.ClientMetadata, roles []string, directory string) error {
	root, err := fsutil.OpenPhysicalRoot(directory, true)
	if err != nil {
		return err
	}
	defer root.Close()
	_, err = readEventBusClients(ctx, metadata, roles, func(name string, max int64) ([]byte, error) { return fsutil.ReadPrivate(root, name, max) })
	return err
}

func readEventBusClients(ctx context.Context, metadata hostbundle.ClientMetadata, roles []string, read func(string, int64) ([]byte, error)) (map[string][]byte, error) {
	fail := func() (map[string][]byte, error) {
		return nil, errors.New("EventBus client inventory or material is invalid")
	}
	if metadata.Version != 1 || metadata.Status != "ok" || !path.IsAbs(metadata.OutputDir) || path.Clean(metadata.OutputDir) != metadata.OutputDir || len(roles) == 0 || len(roles) > 16 || !slices.Equal(metadata.Roles, roles) || !slices.IsSorted(roles) || len(slices.Compact(slices.Clone(roles))) != len(roles) || len(metadata.Files) != len(roles)+1 {
		return fail()
	}
	wanted := []string{"ca.pem"}
	for _, role := range roles {
		if role == "" || len(role) > 64 || strings.ContainsFunc(role, func(r rune) bool { return r != '-' && (r < 'a' || r > 'z') && (r < '0' || r > '9') }) {
			return fail()
		}
		wanted = append(wanted, role+".yaml")
	}
	slices.Sort(wanted)
	files := map[string][]byte{}
	for i, item := range metadata.Files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if item.Path != wanted[i] || item.Size <= 0 || item.Size > 64<<10 {
			return fail()
		}
		raw, err := read(item.Path, item.Size)
		if err != nil {
			return nil, err
		}
		if int64(len(raw)) != item.Size || digest(raw) != item.SHA256 {
			return fail()
		}
		files[item.Path] = raw
	}
	ca := files["ca.pem"]
	if digest(ca) != metadata.CA {
		return fail()
	}
	block, rest := pem.Decode(ca)
	if block == nil || block.Type != "CERTIFICATE" || len(strings.TrimSpace(string(rest))) != 0 {
		return fail()
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !cert.IsCA || cert.CheckSignatureFrom(cert) != nil || time.Now().Before(cert.NotBefore) || !time.Now().Before(cert.NotAfter) {
		return fail()
	}
	raw, err := read("clients.json", 128<<10)
	if err != nil {
		return nil, err
	}
	var stored hostbundle.ClientMetadata
	if canonical(raw, &stored) != nil {
		return fail()
	}
	expected, _ := json.Marshal(metadata)
	actual, _ := json.Marshal(stored)
	if string(expected) != string(actual) {
		return fail()
	}
	files["clients.json"] = raw
	return files, nil
}
