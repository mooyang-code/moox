package unitbundle

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type fixture struct {
	options  Options
	metadata hostbundle.Metadata
	files    map[string][]byte
	ca       *x509.Certificate
	caKey    *ecdsa.PrivateKey
	leaf     *x509.Certificate
	leafKey  *ecdsa.PrivateKey
}

func privateParent(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0o700))
	return dir
}

func certificateInfo(cert *x509.Certificate) hostbundle.CertificateInfo {
	return hostbundle.CertificateInfo{SHA256: digest(cert.Raw), Serial: cert.SerialNumber.Text(16), NotAfter: cert.NotAfter.UTC()}
}

func newFixture(t *testing.T, host string, components []string, operator bool) *fixture {
	t.Helper()
	f := &fixture{
		options: Options{HostID: host, ControlHostID: "control", Address: "192.0.2.2", PrivateAddress: "10.0.0.2", ControlAddress: "192.0.2.1", Components: components, ExpectedHash: strings.Repeat("a", 64), AllowOperator: operator},
		files:   map[string][]byte{},
	}
	now := time.Now().UTC().Truncate(time.Second)
	var err error
	f.caKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-only-root"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(365 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &f.caKey.PublicKey, f.caKey)
	require.NoError(t, err)
	f.ca, err = x509.ParseCertificate(der)
	require.NoError(t, err)
	f.options.ExpectedCA = digest(der)
	f.files["certs/moox-ca.crt"] = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	f.leafKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	f.leaf = &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: host}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(30 * 24 * time.Hour), BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{host}, IPAddresses: []net.IP{net.ParseIP(f.options.Address), net.ParseIP(f.options.PrivateAddress)}}
	f.metadata = hostbundle.Metadata{Version: 1, HostID: host, ControlHostID: "control", BundleDir: "/private-test/host-bundle-original", ConfigFile: "host-gateway/config/app.yaml", ExpectedHash: f.options.ExpectedHash, CA: hostbundle.CAInfo{CertificateInfo: certificateInfo(f.ca)}}
	f.signLeaf(t)
	callers, err := wanted(f.options)
	require.NoError(t, err)
	gatewayKey := ""
	for i, caller := range callers {
		name := caller
		if caller == "host-gateway@"+host {
			name = "host-gateway"
		}
		keyFile := "secrets/caller-" + name + ".key"
		keyID := fmt.Sprintf("test-key-%d", i)
		if caller == "moox-cli" {
			keyFile = "operator/caller-moox-cli.key"
			raw, err := yaml.Marshal(hostbundle.OperatorConfig{Caller: caller, KeyID: keyID, KeyFile: "caller-moox-cli.key"})
			require.NoError(t, err)
			f.files["operator/gateway-client.yaml"] = raw
		}
		f.files[keyFile] = []byte(strings.Repeat(fmt.Sprint(i), 48) + "\n")
		f.metadata.Credentials = append(f.metadata.Credentials, hostbundle.Credential{Caller: caller, KeyID: keyID, KeyFile: keyFile})
		if name == "host-gateway" {
			gatewayKey = keyID
		}
	}
	f.files[f.metadata.ConfigFile], err = hostgatewayconfig.Encode(hostgatewayconfig.Default(host, "control", f.options.ControlAddress, gatewayKey))
	require.NoError(t, err)
	if slices.Contains(components, "access") {
		f.metadata.VerificationFile = "secrets/access/access-verification.json"
		catalog, err := servicecatalog.LoadEmbedded()
		require.NoError(t, err)
		table := accessTable{Version: 1}
		for _, principal := range catalog.Principals {
			// Both current and retiring keys remain valid during rotation.
			for _, generation := range []string{"current", "retiring"} {
				keyID := principal.ID + "-" + generation
				name := "caller-" + principal.ID + "-" + keyID + ".key"
				table.Credentials = append(table.Credentials, accessCredential{KeyID: keyID, Caller: principal.ID, SecretFile: name})
				f.files["secrets/access/"+name] = []byte(strings.Repeat("external-test-secret-", 3) + "\n")
			}
		}
		slices.SortFunc(table.Credentials, func(a, b accessCredential) int {
			return strings.Compare(a.Caller+"\x00"+a.KeyID, b.Caller+"\x00"+b.KeyID)
		})
		raw, err := json.Marshal(table)
		require.NoError(t, err)
		f.files[f.metadata.VerificationFile] = append(raw, '\n')
	}
	f.inventory()
	return f
}

func (f *fixture) signLeaf(t *testing.T) {
	t.Helper()
	der, err := x509.CreateCertificate(rand.Reader, f.leaf, f.ca, &f.leafKey.PublicKey, f.caKey)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	f.metadata.Certificate = certificateInfo(cert)
	f.files["certs/host-gateway/server.crt"] = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	key, err := x509.MarshalPKCS8PrivateKey(f.leafKey)
	require.NoError(t, err)
	f.files["certs/host-gateway/server.key"] = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})
}

func (f *fixture) inventory() {
	f.metadata.Files = nil
	for name, raw := range f.files {
		f.metadata.Files = append(f.metadata.Files, hostbundle.File{Path: name, SHA256: digest(raw), Size: int64(len(raw))})
	}
	slices.SortFunc(f.metadata.Files, func(a, b hostbundle.File) int { return strings.Compare(a.Path, b.Path) })
}

func (f *fixture) encoded(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal(f.metadata)
	require.NoError(t, err)
	return append(raw, '\n')
}

func (f *fixture) write(t *testing.T) string {
	t.Helper()
	directory := privateParent(t)
	for name, raw := range f.files {
		filename := filepath.Join(directory, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(filename), 0o700))
		require.NoError(t, os.WriteFile(filename, raw, 0o600))
	}
	require.NoError(t, os.WriteFile(filepath.Join(directory, "bundle.json"), f.encoded(t), 0o600))
	return directory
}

func (f *fixture) download(t *testing.T) Download {
	t.Helper()
	return func(ctx context.Context, name string, out io.Writer) (int64, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		require.True(t, strings.HasPrefix(name, f.metadata.BundleDir+"/"))
		relative := strings.TrimPrefix(name, f.metadata.BundleDir+"/")
		raw, ok := f.files[relative]
		if relative == "bundle.json" {
			raw, ok = f.encoded(t), true
		}
		if !ok {
			return 0, os.ErrNotExist
		}
		return io.Copy(out, bytes.NewReader(raw))
	}
}

func TestHostMaterialSeparatesTargetAndOperatorIdentities(t *testing.T) {
	for _, target := range []struct {
		host       string
		components []string
		operator   bool
	}{
		{"control", []string{"admin", "console-proxy", "access"}, true},
		{"storage", []string{"storage-primary", "storage-node", "storage-view"}, false},
		{"compute1", []string{"access", "egress-proxy", "trade"}, false},
		{"host-only", nil, false},
	} {
		t.Run(target.host, func(t *testing.T) {
			f := newFixture(t, target.host, target.components, target.operator)
			material, err := Load(t.Context(), f.write(t), f.options)
			require.NoError(t, err)
			require.Equal(t, f.metadata, material.Metadata())
			raw, err := json.Marshal(material)
			require.NoError(t, err)
			for _, output := range []string{string(raw), fmt.Sprint(material), fmt.Sprintf("%#v", material)} {
				require.NotContains(t, output, string(f.files["secrets/caller-host-agent.key"]))
				require.NotContains(t, output, "PRIVATE KEY")
			}
			copy := material.Metadata()
			copy.Files[0].Path = "changed"
			copy.Credentials[0].KeyID = "changed"
			require.Equal(t, f.metadata, material.Metadata())
			destination := filepath.Join(privateParent(t), "services")
			services, err := material.PublishServices(t.Context(), destination)
			require.NoError(t, err)
			serviceOptions := f.options
			serviceOptions.AllowOperator = false
			loaded, err := Load(t.Context(), destination, serviceOptions)
			require.NoError(t, err)
			require.Equal(t, services.Metadata(), loaded.Metadata())
			_, err = os.Lstat(filepath.Join(destination, "operator"))
			require.True(t, os.IsNotExist(err))
		})
	}
}

func TestHostMaterialRejectsWrongTopologyAndTrust(t *testing.T) {
	changes := map[string]func(*fixture){
		"wrong host":            func(f *fixture) { f.options.HostID = "other" },
		"wrong control":         func(f *fixture) { f.options.ControlHostID = "other" },
		"wrong address":         func(f *fixture) { f.options.Address = "192.0.2.99" },
		"wrong private address": func(f *fixture) { f.options.PrivateAddress = "10.0.0.99" },
		"wrong control address": func(f *fixture) { f.options.ControlAddress = "192.0.2.99" },
		"wrong CA":              func(f *fixture) { f.options.ExpectedCA = strings.Repeat("b", 64) },
		"wrong snapshot":        func(f *fixture) { f.options.ExpectedHash = strings.Repeat("b", 64) },
		"incomplete placement":  func(f *fixture) { f.options.Components = nil },
		"control on business":   func(f *fixture) { f.options.Components = []string{"admin"} },
		"operator on business":  func(f *fixture) { f.options.AllowOperator = true },
		"duplicate placement":   func(f *fixture) { f.options.Components = append(f.options.Components, "trade") },
		"root source":           func(f *fixture) { f.metadata.BundleDir = "/" },
		"traversal source":      func(f *fixture) { f.metadata.BundleDir = "/private-test/../other" },
		"unrelated caller":      func(f *fixture) { f.metadata.Credentials[0].Caller = "moox-cli" },
		"missing key ID":        func(f *fixture) { f.metadata.Credentials[0].KeyID = "" },
		"duplicate key ID":      func(f *fixture) { f.metadata.Credentials[0].KeyID = f.metadata.Credentials[1].KeyID },
		"misplaced key":         func(f *fixture) { f.metadata.Credentials[0].KeyFile = "operator/caller-moox-cli.key" },
		"unbounded size":        func(f *fixture) { f.metadata.Files[0].Size = 1 << 21 },
		"path escape":           func(f *fixture) { f.metadata.Files[0].Path = "../secret" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, "compute1", []string{"trade"}, false)
			change(f)
			_, err := Load(t.Context(), f.write(t), f.options)
			require.Error(t, err)
		})
	}
}

func TestHostMaterialRejectsInvalidContentsWithMatchingInventory(t *testing.T) {
	changes := map[string]func(*testing.T, *fixture){
		"wrong SAN": func(t *testing.T, f *fixture) {
			f.leaf.DNSNames = append(f.leaf.DNSNames, "unrelated.example")
			f.signLeaf(t)
		},
		"missing private SAN": func(t *testing.T, f *fixture) { f.leaf.IPAddresses = f.leaf.IPAddresses[:1]; f.signLeaf(t) },
		"wrong common name":   func(t *testing.T, f *fixture) { f.leaf.Subject.CommonName = "other"; f.signLeaf(t) },
		"expired leaf": func(t *testing.T, f *fixture) {
			f.leaf.NotBefore = time.Now().Add(-2 * time.Hour)
			f.leaf.NotAfter = time.Now().Add(-time.Hour)
			f.signLeaf(t)
		},
		"client certificate": func(t *testing.T, f *fixture) {
			f.leaf.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
			f.signLeaf(t)
		},
		"leaf is CA": func(t *testing.T, f *fixture) { f.leaf.IsCA = true; f.signLeaf(t) },
		"wrong private key": func(t *testing.T, f *fixture) {
			other := newFixture(t, "other", nil, false)
			f.files["certs/host-gateway/server.key"] = other.files["certs/host-gateway/server.key"]
		},
		"extra PEM": func(t *testing.T, f *fixture) {
			f.files["certs/moox-ca.crt"] = append(f.files["certs/moox-ca.crt"], f.files["certs/host-gateway/server.key"]...)
		},
		"forbidden master": func(t *testing.T, f *fixture) {
			f.files["secrets/master.key"] = []byte(strings.Repeat("never-export", 4))
		},
		"forbidden CA key": func(t *testing.T, f *fixture) { f.files["certs/ca.key"] = f.files["certs/host-gateway/server.key"] },
		"short secret":     func(t *testing.T, f *fixture) { f.files["secrets/caller-host-agent.key"] = []byte("short\n") },
		"oversized raw secret": func(t *testing.T, f *fixture) {
			f.files["secrets/caller-host-agent.key"] = []byte(strings.Repeat(" ", 4096) + strings.Repeat("a", 32))
		},
		"secret whitespace": func(t *testing.T, f *fixture) {
			f.files["secrets/caller-host-agent.key"] = []byte(strings.Repeat("a", 32) + " internal-space\n")
		},
		"gateway redirected": func(t *testing.T, f *fixture) {
			config := hostgatewayconfig.Default(f.options.HostID, "control", "192.0.2.99", f.metadata.Credentials[1].KeyID)
			raw, err := hostgatewayconfig.Encode(config)
			require.NoError(t, err)
			f.files[f.metadata.ConfigFile] = raw
		},
		"operator redirected": func(t *testing.T, f *fixture) {
			f.files["operator/gateway-client.yaml"] = []byte("caller: admin\nkey_id: other\nkey_file: /tmp/unrelated\n")
		},
		"certificate metadata": func(t *testing.T, f *fixture) { f.metadata.Certificate.Serial = "ffff" },
		"CA metadata":          func(t *testing.T, f *fixture) { f.metadata.CA.Serial = "ffff" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, "control", []string{"admin", "access"}, true)
			change(t, f)
			f.inventory()
			_, err := Load(t.Context(), f.write(t), f.options)
			require.Error(t, err)
		})
	}
}

func TestHostMaterialRejectsAccessIdentityEscapes(t *testing.T) {
	changes := map[string]func(*accessTable, *fixture){
		"internal principal": func(table *accessTable, f *fixture) { table.Credentials[0].Caller = "host-agent" },
		"missing external principal": func(table *accessTable, f *fixture) {
			first := table.Credentials[0].Caller
			table.Credentials = slices.DeleteFunc(table.Credentials, func(e accessCredential) bool { return e.Caller == first })
		},
		"path escape": func(table *accessTable, f *fixture) { table.Credentials[0].SecretFile = "../../caller-host-agent.key" },
		"duplicate entry": func(table *accessTable, f *fixture) {
			table.Credentials = append(table.Credentials, table.Credentials[0])
		},
		"repeated internal key ID": func(table *accessTable, f *fixture) {
			entry := &table.Credentials[0]
			entry.KeyID = f.metadata.Credentials[0].KeyID
			entry.SecretFile = "caller-" + entry.Caller + "-" + entry.KeyID + ".key"
			f.files["secrets/access/"+entry.SecretFile] = []byte(strings.Repeat("a", 48))
		},
		"missing secret": func(table *accessTable, f *fixture) {
			delete(f.files, "secrets/access/"+table.Credentials[0].SecretFile)
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, "compute1", []string{"access"}, false)
			var table accessTable
			require.NoError(t, json.Unmarshal(f.files[f.metadata.VerificationFile], &table))
			change(&table, f)
			raw, err := json.Marshal(table)
			require.NoError(t, err)
			f.files[f.metadata.VerificationFile] = append(raw, '\n')
			f.inventory()
			_, err = Load(t.Context(), f.write(t), f.options)
			require.Error(t, err)
		})
	}
}

func TestHostMaterialRejectsUnsafeFilesAndNoncanonicalJSON(t *testing.T) {
	changes := map[string]func(*testing.T, string, *fixture){
		"file permissions": func(t *testing.T, dir string, f *fixture) {
			require.NoError(t, os.Chmod(filepath.Join(dir, "certs/moox-ca.crt"), 0o644))
		},
		"directory permissions": func(t *testing.T, dir string, f *fixture) {
			require.NoError(t, os.Chmod(filepath.Join(dir, "secrets"), 0o755))
		},
		"root permissions": func(t *testing.T, dir string, f *fixture) { require.NoError(t, os.Chmod(dir, 0o755)) },
		"symlink payload": func(t *testing.T, dir string, f *fixture) {
			name := filepath.Join(dir, "secrets/caller-host-agent.key")
			raw, err := os.ReadFile(name)
			require.NoError(t, err)
			other := filepath.Join(privateParent(t), "other.key")
			require.NoError(t, os.WriteFile(other, raw, 0o600))
			require.NoError(t, os.Remove(name))
			require.NoError(t, os.Symlink(other, name))
		},
		"unlisted file": func(t *testing.T, dir string, f *fixture) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "extra"), []byte("extra"), 0o600))
		},
		"unlisted directory": func(t *testing.T, dir string, f *fixture) {
			require.NoError(t, os.Mkdir(filepath.Join(dir, "extra"), 0o700))
		},
		"bad digest": func(t *testing.T, dir string, f *fixture) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "secrets/caller-host-agent.key"), []byte(strings.Repeat("x", 48)+"\n"), 0o600))
		},
		"duplicate field": func(t *testing.T, dir string, f *fixture) {
			raw := f.encoded(t)
			raw = append([]byte(`{"version":1,`), raw[1:]...)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "bundle.json"), raw, 0o600))
		},
		"unknown field": func(t *testing.T, dir string, f *fixture) {
			raw := f.encoded(t)
			raw = append([]byte(`{"extra":1,`), raw[1:]...)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "bundle.json"), raw, 0o600))
		},
		"multiple documents": func(t *testing.T, dir string, f *fixture) {
			raw := append(f.encoded(t), []byte("{}\n")...)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "bundle.json"), raw, 0o600))
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, "storage", []string{"storage-primary"}, false)
			dir := f.write(t)
			change(t, dir, f)
			_, err := Load(t.Context(), dir, f.options)
			require.Error(t, err)
		})
	}
}

func TestHostMaterialFetchPinsAndBoundsEveryTransfer(t *testing.T) {
	f := newFixture(t, "compute1", []string{"access", "trade"}, false)
	destination := filepath.Join(privateParent(t), "fetched")
	material, err := Fetch(t.Context(), f.download(t), f.metadata, f.options, destination)
	require.NoError(t, err)
	require.Equal(t, destination, material.Directory())
	_, err = Load(t.Context(), destination, f.options)
	require.NoError(t, err)
	for _, scenario := range []string{"overflow", "false count", "wrong digest", "source mismatch", "private error", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			download := f.download(t)
			bad := func(ctx context.Context, name string, out io.Writer) (int64, error) {
				if path.Base(name) == "bundle.json" && scenario != "source mismatch" {
					return download(ctx, name, out)
				}
				switch scenario {
				case "overflow":
					return io.Copy(out, bytes.NewReader(make([]byte, 2<<20)))
				case "false count":
					n, err := download(ctx, name, out)
					return n + 1, err
				case "wrong digest":
					raw := bytes.Repeat([]byte("x"), len(f.files[strings.TrimPrefix(name, f.metadata.BundleDir+"/")]))
					n, err := out.Write(raw)
					return int64(n), err
				case "source mismatch":
					source := f.metadata
					source.Certificate.Serial = "changed"
					raw, err := json.Marshal(source)
					require.NoError(t, err)
					n, err := out.Write(append(raw, '\n'))
					return int64(n), err
				case "private error":
					return 0, errors.New("PRIVATE-MATERIAL-MUST-NOT-LEAK")
				case "cancelled":
					cancel()
					return download(ctx, name, out)
				}
				panic("unexpected scenario")
			}
			destination := filepath.Join(privateParent(t), "failed")
			material, err := Fetch(ctx, bad, f.metadata, f.options, destination)
			require.Error(t, err)
			require.Nil(t, material)
			require.NotContains(t, err.Error(), "PRIVATE-MATERIAL-MUST-NOT-LEAK")
			entries, err := os.ReadDir(filepath.Dir(destination))
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
	badOptions := f.options
	badOptions.ExpectedCA = strings.Repeat("b", 64)
	_, err = Fetch(t.Context(), func(context.Context, string, io.Writer) (int64, error) {
		t.Fatal("must reject before any download")
		return 0, nil
	}, f.metadata, badOptions, filepath.Join(privateParent(t), "bad"))
	require.Error(t, err)
}

func TestHostMaterialPublicationPreservesExistingObjectsAndConcurrentWinner(t *testing.T) {
	f := newFixture(t, "host-only", nil, false)
	for _, kind := range []string{"file", "directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			parent := privateParent(t)
			destination := filepath.Join(parent, "existing")
			switch kind {
			case "file":
				require.NoError(t, os.WriteFile(destination, []byte("keep"), 0o600))
			case "directory":
				require.NoError(t, os.Mkdir(destination, 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(destination, "marker"), []byte("keep"), 0o600))
			case "symlink":
				require.NoError(t, os.Symlink("missing-target", destination))
			}
			before, err := os.Lstat(destination)
			require.NoError(t, err)
			material, err := Fetch(t.Context(), f.download(t), f.metadata, f.options, destination)
			require.Error(t, err)
			require.Nil(t, material)
			after, err := os.Lstat(destination)
			require.NoError(t, err)
			require.True(t, os.SameFile(before, after))
			entries, err := os.ReadDir(parent)
			require.NoError(t, err)
			require.Len(t, entries, 1)
		})
	}
	destination := filepath.Join(privateParent(t), "winner")
	var wait sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wait.Go(func() {
			_, err := Fetch(t.Context(), f.download(t), f.metadata, f.options, destination)
			results <- err
		})
	}
	wait.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	require.Equal(t, 1, success)
	_, err := Load(t.Context(), destination, f.options)
	require.NoError(t, err)
	entries, err := os.ReadDir(filepath.Dir(destination))
	require.NoError(t, err)
	require.Len(t, entries, 1)
}
