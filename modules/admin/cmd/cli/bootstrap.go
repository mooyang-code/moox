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
	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	adminschema "github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/security"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
	"gorm.io/gorm"
)

type bootstrapOptions struct {
	topologyFile, dbPath, masterFile, pkiDir, outputDir string
	// operatorPublicKey 是操作电脑的 moox-cli 公钥：只在还没登记时写入，重复执行不改已有的。
	operatorPublicKey string
	current           currentKeys
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
	fs.StringVar(&opts.operatorPublicKey, "operator-public-key", "", "操作电脑的 moox-cli 公钥（base64）；还没登记时写入")
	registerCurrentKeyFlags(fs, &opts.current)
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if _, err := gatewayauth.ParsePublicKey(opts.operatorPublicKey); err != nil {
		return errors.New("bootstrap 需要 --operator-public-key：操作电脑的 moox-cli 公钥（先运行 moox-cli setup operator）")
	}
	if err := opts.current.validate(); err != nil {
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

func readBootstrapTopology(path string) (hostbundle.Topology, error) {
	raw, err := privatefiles.Read(path, 1<<20)
	if err != nil {
		return hostbundle.Topology{}, fmt.Errorf("read normalized topology: %w", err)
	}
	var topology hostbundle.Topology
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&topology) != nil {
		return topology, errors.New("invalid normalized topology JSON or unknown fields")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF || topology.Version != 1 || !servicecatalog.ValidHostID(topology.ControlHostID) || len(topology.Hosts) == 0 || len(topology.Hosts) > 1024 {
		return topology, errors.New("normalized topology requires version 1, canonical control_host_id and 1..1024 hosts in exactly one JSON document")
	}
	if !slices.ContainsFunc(topology.Hosts, func(h hostbundle.Host) bool { return h.HostID == topology.ControlHostID }) {
		return topology, errors.New("normalized topology must include the control host")
	}
	return topology, nil
}

func bootstrapAdmin(ctx context.Context, opts bootstrapOptions, topology hostbundle.Topology, stdout io.Writer) error {
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
	var caInfo hostbundle.CAInfo
	var ca *pki.Store
	defer func() {
		if ca != nil {
			ca.Close()
		}
	}()
	control := topology.Hosts[slices.IndexFunc(topology.Hosts, func(h hostbundle.Host) bool { return h.HostID == topology.ControlHostID })]
	material := hostBundleMaterial{host: control, control: control}
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 主机的 SSH 凭据加密要用主密钥：同步校验通过、要写凭据时才取得（没有则创建），拓扑非法时不会创建它。
		var master string
		masterKey := func() (string, error) {
			if master != "" {
				return master, nil
			}
			value, err := bootstrapMaster(tx, masterRoot, filepath.Base(opts.masterFile))
			master = value
			return value, err
		}
		dao, err := sysdeploy.NewTopologyDAO(tx, topology.ControlHostID)
		if err != nil {
			return err
		}
		dao.WithEncryptionKeyFunc(masterKey)
		specs := make([]sysdeploy.HostSpec, 0, len(topology.Hosts))
		for _, h := range topology.Hosts {
			specs = append(specs, hostSpec(h))
		}
		// 按 moox.toml 整份覆盖主机与部署（含启停状态）；同时先占住 SQLite 的写锁再校验整份拓扑。
		if _, err := dao.ReplaceTopology(ctx, specs); err != nil {
			return err
		}
		if _, err := masterKey(); err != nil {
			return err
		}
		store, err := keys.NewStore(tx)
		if err != nil {
			return err
		}
		// 库里已经登记过签名公钥、CA 目录却没了：不能悄悄换一个新的信任根，要先把原来的 CA 恢复回来。
		var previousKeys int64
		if err := tx.Table("t_hosts").Where("c_sign_public_key <> ''").Count(&previousKeys).Error; err != nil {
			return err
		}
		if previousKeys > 0 {
			if _, err := os.Lstat(filepath.Join(opts.pkiDir, "ca.crt")); err != nil {
				return pki.ErrInvalidCA
			}
		}
		if material.operatorPublic, err = store.EnsureIdentity(ctx, servicecatalog.OperatorMooxCLI, opts.operatorPublicKey); err != nil {
			return err
		}
		if material.keys, err = ensureSigningKeys(ctx, store, control, opts.current, false); err != nil {
			return err
		}
		// 拓扑要合法、能算出控制机的路由，公钥要能拼出快照，否则不提交。
		if _, err := dao.Compile(ctx, control.HostID); err != nil {
			return err
		}
		if _, err := dao.Snapshot(ctx); err != nil {
			return err
		}
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
	// 主密钥与 CA 留在控制机上；拓扑与公钥一起提交之后才发布身份包。
	material.caInfo = caInfo
	result, err := publishHostBundle(opts.outputDir, material, ca)
	if err != nil {
		return err
	}
	return writeJSON(stdout, result)
}

// hostSpec 把离线拓扑里的主机转换成部署同步的输入；SSH 凭据随主机一起写入。
func hostSpec(h hostbundle.Host) sysdeploy.HostSpec {
	spec := sysdeploy.HostSpec{HostID: h.HostID, Enabled: !h.Disabled, Label: h.Label, Address: h.Address, PrivateAddress: h.PrivateAddress, Region: h.Region, Reachability: h.Reachability,
		ExternalEntry: h.ExternalEntry, Description: h.Description, Components: h.Components, DisabledComponents: h.DisabledComponents}
	if h.SSH != nil && h.SSH.Port != 0 {
		ssh := &sysdeploy.SSHSpec{Port: h.SSH.Port, User: h.SSH.User}
		if h.SSH.AuthType == "private_key" {
			ssh.PrivateKey, ssh.Passphrase = h.SSH.Secret, h.SSH.Passphrase
		} else {
			ssh.Password = h.SSH.Secret
		}
		spec.SSH = ssh
	}
	return spec
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
