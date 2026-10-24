// Package dao 读取 SSH 连接需要的主机登记与凭据，并记录主机密钥指纹。
package dao

import (
	"context"
	"errors"
	"fmt"
	"strings"

	adminsecurity "github.com/mooyang-code/moox/modules/admin/internal/security"
	secretdao "github.com/mooyang-code/moox/modules/admin/internal/service/secret/dao"
	secretmodel "github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	"github.com/mooyang-code/moox/modules/admin/internal/service/ssh/model"
	"gorm.io/gorm"
)

var (
	// ErrHostNotFound 主机没有登记。
	ErrHostNotFound = errors.New("主机未登记")
	// ErrSSHUnavailable 主机没有登记控制台可用的 SSH 信息（只能出站的主机，或没有配置密码、私钥）。
	ErrSSHUnavailable = errors.New("该主机没有登记控制台 SSH 信息，请通过 moox skill 配置后再同步")
)

type hostRow struct {
	HostID         string `gorm:"column:c_host_id"`
	Address        string `gorm:"column:c_address"`
	Reachability   string `gorm:"column:c_reachability"`
	SSHPort        int    `gorm:"column:c_ssh_port"`
	SSHUser        string `gorm:"column:c_ssh_user"`
	SSHFingerprint string `gorm:"column:c_ssh_fingerprint"`
}

func (hostRow) TableName() string { return "t_hosts" }

// HostDAO 读取 t_hosts 里的 SSH 列与 t_secrets 里的凭据。
type HostDAO struct {
	db *gorm.DB
}

func NewHostDAO(db *gorm.DB) *HostDAO { return &HostDAO{db: db} }

// Target 合成一次连接需要的信息。
func (d *HostDAO) Target(ctx context.Context, hostID string) (model.Target, error) {
	var row hostRow
	if err := d.db.WithContext(ctx).Where("c_host_id = ?", hostID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Target{}, ErrHostNotFound
		}
		return model.Target{}, err
	}
	if row.SSHPort == 0 || row.Reachability == "outbound" {
		return model.Target{}, ErrSSHUnavailable
	}
	key, err := adminsecurity.GetEncryptionKey()
	if err != nil {
		return model.Target{}, err
	}
	secret, err := secretdao.GetSystemSecret(d.db.WithContext(ctx), key, secretmodel.SSHHostSecretID(hostID))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.Target{}, ErrSSHUnavailable
	}
	if err != nil {
		return model.Target{}, fmt.Errorf("读取主机 SSH 凭据: %w", err)
	}
	credential, err := secretmodel.DecodeSSHCredential(secret.SecretValue)
	if err != nil {
		return model.Target{}, err
	}
	return model.Target{HostID: row.HostID, Address: row.Address, Port: row.SSHPort, User: row.SSHUser, Credential: credential}, nil
}

// PinHostKey 原子地确定主机在该地址与端口上受信任的指纹：没有记录或记录属于别的地址、端口时记录新的指纹，
// 已有同一地址与端口的记录则保持不变。返回生效的指纹。
// 指纹以“地址:端口 指纹”存进 t_hosts.c_ssh_fingerprint；单条带条件的 UPDATE 保证并发的首次连接只有一个会写入，
// 其余读到的是它写入的值，所以不需要按主机加锁，改地址或端口也不会被旧连接把旧指纹写回。
func (d *HostDAO) PinHostKey(hostID, endpoint, fingerprint string) (string, error) {
	prefix := endpoint + " "
	if err := d.db.Exec(`UPDATE t_hosts SET c_ssh_fingerprint = ?
		WHERE c_host_id = ? AND (c_ssh_fingerprint = '' OR substr(c_ssh_fingerprint, 1, ?) <> ?)`,
		prefix+fingerprint, hostID, len(prefix), prefix).Error; err != nil {
		return "", err
	}
	var rows []struct{ Fingerprint string }
	if err := d.db.Raw("SELECT c_ssh_fingerprint AS fingerprint FROM t_hosts WHERE c_host_id = ?", hostID).Scan(&rows).Error; err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", ErrHostNotFound
	}
	pinned, ok := strings.CutPrefix(rows[0].Fingerprint, prefix)
	if !ok {
		return "", errors.New("host key pin was not recorded")
	}
	return pinned, nil
}
