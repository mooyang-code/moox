// Package dao 读取 SSH 连接需要的主机登记与凭据，并记录主机密钥指纹。
package dao

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
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

// ErrHostChanged 主机的地址或 SSH 端口在连接期间被改动，这条连接不再有效。
var ErrHostChanged = errors.New("主机的连接信息已变化，请重新连接")

// PinHostKey 原子地确定主机在“地址:端口”上受信任的指纹，并且只认主机当前登记的端点：
//   - 端点不是当前登记的（连接期间被同步改了地址或端口）：返回 ErrHostChanged，不写任何东西，旧连接无法把旧指纹写回；
//   - 记录为空，或记录属于别的端点：记录这次看到的指纹（首次信任）；
//   - 已有同一端点的记录：保持不变。
//
// 返回生效的指纹。指纹以“地址:端口 指纹”存进 t_hosts.c_ssh_fingerprint；带条件的单条 UPDATE 保证并发的首次连接只有一个会写入，
// 其余读到的是它写入的值，所以不需要按主机加锁。
func (d *HostDAO) PinHostKey(hostID, endpoint, fingerprint string) (string, error) {
	address, portText, err := net.SplitHostPort(endpoint)
	if err != nil {
		return "", fmt.Errorf("无效的主机端点 %q: %w", endpoint, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return "", fmt.Errorf("无效的主机端点 %q: %w", endpoint, err)
	}
	prefix := endpoint + " "
	if err := d.db.Exec(`UPDATE t_hosts SET c_ssh_fingerprint = ?
		WHERE c_host_id = ? AND c_address = ? AND c_ssh_port = ?
		AND (c_ssh_fingerprint = '' OR substr(c_ssh_fingerprint, 1, ?) <> ?)`,
		prefix+fingerprint, hostID, address, port, len(prefix), prefix).Error; err != nil {
		return "", err
	}
	var rows []hostRow
	if err := d.db.Where("c_host_id = ?", hostID).Find(&rows).Error; err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", ErrHostNotFound
	}
	if rows[0].Address != address || rows[0].SSHPort != port {
		return "", ErrHostChanged
	}
	pinned, ok := strings.CutPrefix(rows[0].SSHFingerprint, prefix)
	if !ok {
		return "", errors.New("host key pin was not recorded")
	}
	return pinned, nil
}
