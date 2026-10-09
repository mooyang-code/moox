package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mooyang-code/moox/modules/admin/internal/pki"
	"github.com/mooyang-code/moox/modules/admin/internal/privatefiles"
	"github.com/mooyang-code/moox/modules/admin/internal/service/keys"
	secretmodel "github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	adminschema "github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/mooyang-code/moox/packages/security"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gorm.io/gorm"
)

// The deployment CLI writes this normalized input, rather than sending its
// private moox.toml (SSH credentials and unrelated deployment secrets).
type bootstrapTopology struct {
	Version       int             `json:"version"`
	ControlHostID string          `json:"control_host_id"`
	Hosts         []bootstrapHost `json:"hosts"`
}

type bootstrapHost struct {
	HostID         string   `json:"host_id"`
	Address        string   `json:"address"`
	PrivateAddress string   `json:"private_address"`
	Region         string   `json:"region"`
	Description    string   `json:"description"`
	Components     []string `json:"components"`
}

type bootstrapOptions struct {
	topologyFile, dbPath, masterFile, pkiDir, outputDir string
}

func isBootstrapCommand(args []string) bool { return len(args) > 1 && args[1] == "bootstrap" }

func runBootstrapCommand(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "bootstrap" {
		return errors.New("expected bootstrap command")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	fs := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var opts bootstrapOptions
	fs.StringVar(&opts.topologyFile, "topology-file", "", "normalized 0600 topology JSON from moox-cli")
	fs.StringVar(&opts.dbPath, "db-path", defaultInitDBPath, "Admin SQLite database")
	fs.StringVar(&opts.masterFile, "encryption-key-file", "", "persistent 0600 Admin encryption key; generated only for an empty secret table")
	fs.StringVar(&opts.pkiDir, "pki-dir", "", "persistent 0700 control CA directory")
	fs.StringVar(&opts.outputDir, "output-dir", "", "0700 parent directory for a complete new bootstrap bundle")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected bootstrap arguments")
	}
	for _, path := range []*string{&opts.topologyFile, &opts.dbPath, &opts.masterFile, &opts.pkiDir, &opts.outputDir} {
		if *path == "" || *path != strings.TrimSpace(*path) || strings.ContainsAny(*path, "\x00\r\n?#") || strings.HasPrefix(*path, "file:") {
			return errors.New("bootstrap requires ordinary --topology-file, --db-path, --encryption-key-file, --pki-dir and --output-dir paths")
		}
		abs, err := filepath.Abs(*path)
		if err != nil {
			return err
		}
		*path = abs
	}
	if opts.dbPath == opts.masterFile || opts.dbPath == opts.topologyFile || opts.masterFile == opts.topologyFile {
		return errors.New("database, encryption key and topology input must use different files")
	}
	topology, err := readBootstrapTopology(opts.topologyFile)
	if err != nil {
		return err
	}
	return bootstrapAdmin(context.Background(), opts, topology, stdout)
}

func readBootstrapTopology(path string) (bootstrapTopology, error) {
	raw, err := privatefiles.Read(path, 1<<20)
	if err != nil {
		return bootstrapTopology{}, fmt.Errorf("read normalized topology: %w", err)
	}
	var topology bootstrapTopology
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&topology) != nil {
		return topology, errors.New("invalid normalized topology JSON or unknown fields")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF || topology.Version != 1 || !servicecatalog.ValidHostID(topology.ControlHostID) || len(topology.Hosts) == 0 || len(topology.Hosts) > 1024 {
		return topology, errors.New("normalized topology requires version 1, canonical control_host_id and 1..1024 hosts in exactly one JSON document")
	}
	if !slices.ContainsFunc(topology.Hosts, func(h bootstrapHost) bool { return h.HostID == topology.ControlHostID }) {
		return topology, errors.New("normalized topology must include the control host")
	}
	return topology, nil
}

func bootstrapAdmin(ctx context.Context, opts bootstrapOptions, topology bootstrapTopology, stdout io.Writer) error {
	// Serialize the entire operation across processes, before opening SQLite or
	// creating a master key. The persistent file itself is never a stale lock.
	masterRoot, err := privatefiles.OpenRoot(filepath.Dir(opts.masterFile))
	if err != nil {
		return err
	}
	defer masterRoot.Close()
	lock, err := privatefiles.Lock(masterRoot, ".bootstrap-"+filepath.Base(opts.masterFile)+".lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := prepareBootstrapDatabase(opts.dbPath); err != nil {
		return err
	}
	db, err := openAdminCLIDB(opts.dbPath)
	if err != nil {
		return err
	}
	defer closeAdminCLIDB(db)
	if err := db.Transaction(func(tx *gorm.DB) error { return tx.Exec(adminschema.AdminSQL()).Error }); err != nil {
		return fmt.Errorf("apply Admin schema: %w", err)
	}
	var exported []keys.SigningKey
	var expectedHash string
	var caInfo pki.CAInfo
	var ca *pki.Store
	defer func() {
		if ca != nil {
			ca.Close()
		}
	}()
	control := topology.Hosts[slices.IndexFunc(topology.Hosts, func(h bootstrapHost) bool { return h.HostID == topology.ControlHostID })]
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		dao, err := sysdeploy.NewTopologyDAO(tx, topology.ControlHostID)
		if err != nil {
			return err
		}
		specs := make([]sysdeploy.HostSpec, 0, len(topology.Hosts))
		for _, h := range topology.Hosts {
			specs = append(specs, sysdeploy.HostSpec{HostID: h.HostID, Address: h.Address, PrivateAddress: h.PrivateAddress, Region: h.Region, Description: h.Description, Components: h.Components})
		}
		// This also reserves SQLite's writer before validating the full fleet.
		if err := dao.SyncHosts(ctx, specs); err != nil {
			return err
		}
		master, err := bootstrapMaster(tx, masterRoot, filepath.Base(opts.masterFile))
		if err != nil {
			return err
		}
		store, err := keys.NewStore(tx, master)
		if err != nil {
			return err
		}
		// A database with established gateway keys cannot silently acquire a
		// replacement trust root even if the entire PKI directory has been lost.
		var previousKeys int64
		if err := tx.Table("t_secrets").Where("c_secret_type = ?", secretmodel.GatewayKeyringType).Count(&previousKeys).Error; err != nil {
			return err
		}
		if previousKeys > 0 {
			if _, err := os.Lstat(filepath.Join(opts.pkiDir, "ca.crt")); err != nil {
				return pki.ErrInvalidCA
			}
		}
		all, err := store.EnsureAll(ctx)
		if err != nil {
			return err
		}
		wanted := append([]string{"console", "moox-cli", "host-agent", "host-gateway@" + control.HostID}, control.Components...)
		for _, key := range all {
			if slices.Contains(wanted, key.Caller) {
				exported = append(exported, key)
			}
		}
		_, snapshot, err := dao.CompileSnapshot(ctx, control.HostID, master)
		if err != nil {
			return err
		}
		expectedHash = snapshot.Hash
		ca, err = pki.Open(opts.pkiDir)
		if err != nil {
			return err
		}
		caInfo, err = ca.EnsureCA()
		return err
	})
	if err != nil {
		return err
	}
	// The master and CA are persistent retry checkpoints. Publish a bundle only
	// after the topology and all encrypted caller keys have committed together.
	result, err := publishBootstrapBundle(opts.outputDir, control, exported, ca, caInfo, expectedHash)
	if err != nil {
		return err
	}
	return writeJSON(stdout, result)
}

func bootstrapMaster(tx *gorm.DB, root *os.Root, name string) (string, error) {
	master, err := privatefiles.ReadAt(root, name, 8192)
	if err == nil {
		value := strings.TrimSpace(string(master))
		if len(value) < 32 || strings.ContainsAny(value, "\x00\r\n") {
			return "", errors.New("Admin encryption key must contain at least 32 characters on one line")
		}
		return value, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	var count int64
	if err := tx.Table("t_secrets").Count(&count).Error; err != nil {
		return "", err
	}
	if count != 0 {
		return "", errors.New("Admin encryption key is missing but encrypted secrets exist; restore the original key")
	}
	value, err := security.RandomHex(32)
	if err != nil {
		return "", err
	}
	if err := privatefiles.Write(root, name, []byte(value+"\n")); err != nil {
		return "", err
	}
	return value, nil
}

func prepareBootstrapDatabase(path string) error {
	root, err := privatefiles.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer root.Close()
	name := filepath.Base(path)
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err == nil {
		return file.Close()
	}
	if !os.IsExist(err) {
		return err
	}
	return validateExistingAdminDatabase(path)
}

func validateExistingAdminDatabase(path string) error {
	if path == "" || path != strings.TrimSpace(path) || strings.ContainsAny(path, "\x00\r\n?#") || strings.HasPrefix(path, "file:") {
		return errors.New("Admin database requires an ordinary file path")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat existing Admin database: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return errors.New("Admin database must be an existing regular 0600 file")
	}
	return nil
}
