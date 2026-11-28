package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/mooyang-code/moox/modules/admin/internal/pki"
	"github.com/mooyang-code/moox/modules/admin/internal/privatefiles"
	"github.com/mooyang-code/moox/modules/admin/internal/service/keys"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/security"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
)

// currentKeys 是目标机上现有私钥对应的公钥（由部署命令在目标机上算出后传来）；没有私钥时为空。
type currentKeys struct{ host, scfCollector string }

// signingKeys 是一台主机这次身份包里的签名材料。私钥只在新签发时有，写进身份包后 Admin 不再保留。
type signingKeys struct {
	hostPublic, scfCollectorPublic string
	hostKey, scfCollectorKey       []byte
}

// bundleHost 是签发主机身份包与证书所需的主机摘要信息。
type bundleHost struct {
	hostID, address, privateAddress, reachability string
	components                                    []string
}

type hostBundleMaterial struct {
	host, control bundleHost
	keys          signingKeys
	// operatorPublic 是登记着的 moox-cli 公钥，只有 bootstrap 填。
	operatorPublic string
	caInfo         hostbundle.CAInfo
}

func newSigningKey() (public string, file []byte, err error) {
	key, err := gatewayauth.GenerateSigningKey()
	if err != nil {
		return "", nil, err
	}
	return gatewayauth.EncodePublicKey(gatewayauth.Credentials{Key: key}.PublicKey()), []byte(gatewayauth.EncodeSigningKey(key) + "\n"), nil
}

// ensureSigningKeys 决定这台主机的签名密钥：目标机上现有私钥的公钥和登记的一致就原样沿用（例行升级、续证书都不换密钥）；
// 不一致、目标机上没有私钥，或显式要求（rotate）时新生成一把并登记公钥，旧私钥随即作废，没有新旧并存。
// 部署了 collector 的主机另管 scf-collector 的私钥，规则相同（rotate 只换主机密钥）。
func ensureSigningKeys(ctx context.Context, store *keys.Store, host bundleHost, current currentKeys, rotate bool) (signingKeys, error) {
	var result signingKeys
	registered, err := store.HostPublicKey(ctx, host.hostID)
	if err != nil {
		return result, err
	}
	if rotate || current.host == "" || current.host != registered {
		public, file, err := newSigningKey()
		if err != nil {
			return result, err
		}
		if err := store.SetHostPublicKey(ctx, host.hostID, public); err != nil {
			return result, err
		}
		registered, result.hostKey = public, file
	}
	result.hostPublic = registered
	if !slices.Contains(host.components, "collector") {
		return result, nil
	}
	registered, err = store.Identity(ctx, "scf-collector")
	if err != nil && !errors.Is(err, keys.ErrNotRegistered) {
		return result, err
	}
	if current.scfCollector == "" || current.scfCollector != registered {
		public, file, err := newSigningKey()
		if err != nil {
			return result, err
		}
		if err := store.SetIdentity(ctx, "scf-collector", public); err != nil {
			return result, err
		}
		registered, result.scfCollectorKey = public, file
	}
	result.scfCollectorPublic = registered
	return result, nil
}

// publishHostBundle 把一台主机的身份包写到一个新的私有目录：主机签名私钥（新签发时）、主机证书、根证书，
// 控制机另有 scf-collector 的私钥（新签发时）。目录先写在点开头的临时名下，写全了才改名，不会留下半个包。
// Admin 主密钥与 CA 私钥不进包。
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
	result := hostbundle.Metadata{
		Version: 2, HostID: material.host.hostID, ControlHostID: material.control.hostID,
		BundleDir:     filepath.Join(parentDir, finalName),
		SignPublicKey: material.keys.hostPublic, SignKeyIssued: material.keys.hostKey != nil,
		SCFCollectorPublicKey: material.keys.scfCollectorPublic, SCFCollectorKeyIssued: material.keys.scfCollectorKey != nil,
		OperatorPublicKey: material.operatorPublic, CA: material.caInfo,
	}
	for name, raw := range map[string][]byte{hostbundle.HostKeyFile: material.keys.hostKey, hostbundle.SCFCollectorKeyFile: material.keys.scfCollectorKey} {
		if raw == nil {
			continue
		}
		if err := privatefiles.Write(stage, name, raw); err != nil {
			return hostbundle.Metadata{}, err
		}
	}
	issued, err := ca.Issue(pki.HostIdentity{HostID: material.host.hostID, Address: material.host.address, PrivateAddress: material.host.privateAddress, Outbound: material.host.reachability != ""}, filepath.Join(parentDir, stageName, "tls"))
	if err != nil {
		return hostbundle.Metadata{}, err
	}
	if issued.CAFingerprint != material.caInfo.SHA256 {
		return hostbundle.Metadata{}, errors.New("MooX CA changed while issuing the host certificate")
	}
	result.Certificate = issued.CertificateInfo
	for from, to := range map[string]string{"tls/server.crt": hostbundle.HostCertificateFile, "tls/server.key": hostbundle.HostTLSKeyFile, "tls/moox-ca.crt": hostbundle.CAFile} {
		if err := stage.Rename(from, to); err != nil {
			return hostbundle.Metadata{}, err
		}
	}
	if err := stage.Remove("tls"); err != nil {
		return hostbundle.Metadata{}, err
	}
	result.Files, err = hostBundleInventory(stage)
	if err != nil {
		return hostbundle.Metadata{}, err
	}
	// 清单里只有公开的身份、路径与摘要。
	metadata, err := json.Marshal(result)
	if err != nil {
		return hostbundle.Metadata{}, err
	}
	if err := privatefiles.Write(stage, "bundle.json", append(metadata, '\n')); err != nil {
		return hostbundle.Metadata{}, err
	}
	if err := stage.Close(); err != nil {
		return hostbundle.Metadata{}, err
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
