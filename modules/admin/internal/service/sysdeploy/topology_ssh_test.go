package sysdeploy

import (
	"context"
	"errors"
	"testing"

	secretdao "github.com/mooyang-code/moox/modules/admin/internal/service/secret/dao"
	secretmodel "github.com/mooyang-code/moox/modules/admin/internal/service/secret/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const sshTestKey = "0123456789abcdef0123456789abcdef"

func sshTopologyDAO(t *testing.T) *TopologyDAO {
	t.Helper()
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", sshTestKey)
	return topologyDAO(t)
}

func readHost(t *testing.T, dao *TopologyDAO, hostID string) HostRecord {
	t.Helper()
	hosts, _, err := dao.Read(context.Background())
	require.NoError(t, err)
	for _, h := range hosts {
		if h.HostID == hostID {
			return h
		}
	}
	t.Fatalf("host %s not found", hostID)
	return HostRecord{}
}

func TestSyncWritesLabelSSHColumnsAndEncryptedCredential(t *testing.T) {
	dao, ctx := sshTopologyDAO(t), context.Background()
	spec := topologySpecs()[1]
	spec.Label = "存储机"
	spec.SSH = &SSHSpec{Port: 2222, User: "ubuntu", PrivateKey: "-----PRIVATE KEY-----", Passphrase: "phrase"}
	require.NoError(t, dao.SyncHostDeployments(ctx, spec))

	host := readHost(t, dao, "storage")
	require.Equal(t, "存储机", host.Label)
	require.Equal(t, 2222, host.SSHPort)
	require.Equal(t, "ubuntu", host.SSHUser)

	var stored secretmodel.Secret
	require.NoError(t, dao.db.Where("c_secret_id = ?", secretmodel.SSHHostSecretID("storage")).First(&stored).Error)
	require.Equal(t, secretmodel.SSHCategory, stored.Category)
	require.Equal(t, secretmodel.SSHSecretTypeKey, stored.SecretType)
	require.Equal(t, "ubuntu", stored.KeyID)
	require.NotContains(t, stored.SecretValue, "PRIVATE KEY", "凭据必须加密存储")
	plain, err := secretdao.GetSystemSecret(dao.db, sshTestKey, secretmodel.SSHHostSecretID("storage"))
	require.NoError(t, err)
	credential, err := secretmodel.DecodeSSHCredential(plain.SecretValue)
	require.NoError(t, err)
	require.Equal(t, "-----PRIVATE KEY-----", credential.PrivateKey)
	require.Equal(t, "phrase", credential.Passphrase)

	// 换成密码：整条记录被替换，类型随之改变。
	spec.SSH = &SSHSpec{Port: 22, User: "ops", Password: "p4ss"}
	require.NoError(t, dao.SyncHostDeployments(ctx, spec))
	plain, err = secretdao.GetSystemSecret(dao.db, sshTestKey, secretmodel.SSHHostSecretID("storage"))
	require.NoError(t, err)
	require.Equal(t, secretmodel.SSHSecretTypePassword, plain.SecretType)
	require.Equal(t, "ops", plain.KeyID)
}

func TestSyncWithoutSSHClearsPortUserAndCredentialButKeepsFingerprint(t *testing.T) {
	dao, ctx := sshTopologyDAO(t), context.Background()
	spec := topologySpecs()[1]
	spec.SSH = &SSHSpec{Port: 22, User: "ubuntu", Password: "p4ss"}
	require.NoError(t, dao.SyncHostDeployments(ctx, spec))
	require.NoError(t, dao.db.Exec("UPDATE t_hosts SET c_ssh_fingerprint = '198.51.100.7:22 SHA256:abc' WHERE c_host_id = 'storage'").Error)

	spec.SSH = nil
	require.NoError(t, dao.SyncHostDeployments(ctx, spec))
	host := readHost(t, dao, "storage")
	require.Zero(t, host.SSHPort)
	require.Empty(t, host.SSHUser)
	require.Equal(t, "198.51.100.7:22 SHA256:abc", host.SSHFingerprint, "同步不改指纹；指纹按地址端口记录，换端点后自然失效")
	_, err := secretdao.GetSystemSecret(dao.db, sshTestKey, secretmodel.SSHHostSecretID("storage"))
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestSyncRejectsInvalidSSHAndLabel(t *testing.T) {
	dao, ctx := sshTopologyDAO(t), context.Background()
	storage := topologySpecs()[1]
	outbound := HostSpec{HostID: "factor", Address: "192.168.0.103", Reachability: "outbound", Components: []string{"factor-engine"}}
	for name, spec := range map[string]HostSpec{
		"outbound with ssh":   {HostID: outbound.HostID, Address: outbound.Address, Reachability: "outbound", Components: outbound.Components, SSH: &SSHSpec{Port: 22, User: "u", Password: "p"}},
		"no credential":       withSSH(storage, &SSHSpec{Port: 22, User: "u"}),
		"both credentials":    withSSH(storage, &SSHSpec{Port: 22, User: "u", Password: "p", PrivateKey: "k"}),
		"passphrase with pwd": withSSH(storage, &SSHSpec{Port: 22, User: "u", Password: "p", Passphrase: "x"}),
		"bad port":            withSSH(storage, &SSHSpec{Port: 70000, User: "u", Password: "p"}),
		"no user":             withSSH(storage, &SSHSpec{Port: 22, Password: "p"}),
	} {
		err := dao.SyncHostDeployments(ctx, spec)
		require.ErrorIs(t, err, ErrInvalidTopology, name)
	}
	long := storage
	long.Label = string(make([]rune, 65))
	require.ErrorIs(t, dao.SyncHostDeployments(ctx, long), ErrInvalidTopology)
	require.Zero(t, readHost(t, dao, "storage").SSHPort)
}

func withSSH(spec HostSpec, ssh *SSHSpec) HostSpec {
	spec.SSH = ssh
	return spec
}

func TestInvalidSSHRollsBackWholeSync(t *testing.T) {
	dao, ctx := sshTopologyDAO(t), context.Background()
	valid := withSSH(topologySpecs()[1], &SSHSpec{Port: 22, User: "u", Password: "p"})
	bad := withSSH(topologySpecs()[2], &SSHSpec{Port: 22, User: "u"})
	err := dao.SyncHosts(ctx, []HostSpec{valid, bad})
	require.ErrorIs(t, err, ErrInvalidTopology)
	require.Zero(t, readHost(t, dao, "storage").SSHPort, "同一批里有非法 SSH 信息时，整批不写入")
	_, err = secretdao.GetSystemSecret(dao.db, sshTestKey, secretmodel.SSHHostSecretID("storage"))
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestDeleteHostRemovesSSHCredential(t *testing.T) {
	dao, ctx := sshTopologyDAO(t), context.Background()
	spec := withSSH(topologySpecs()[2], &SSHSpec{Port: 22, User: "u", Password: "p"})
	require.NoError(t, dao.SyncHostDeployments(ctx, spec))
	// 去掉业务组件后才能删除主机。
	empty := spec
	empty.Components = nil
	require.NoError(t, dao.SyncHostDeployments(ctx, empty))
	require.NoError(t, dao.DeleteHost(ctx, "overseas"))
	_, err := secretdao.GetSystemSecret(dao.db, sshTestKey, secretmodel.SSHHostSecretID("overseas"))
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestLazyEncryptionKeyIsNotRequestedWithoutCredentials(t *testing.T) {
	dao := topologyDAO(t)
	calls := 0
	dao.WithEncryptionKeyFunc(func() (string, error) { calls++; return sshTestKey, nil })
	require.NoError(t, dao.SyncHostDeployments(context.Background(), topologySpecs()[1]))
	require.Zero(t, calls, "没有 SSH 凭据时不取主密钥")
	require.NoError(t, dao.SyncHostDeployments(context.Background(), withSSH(topologySpecs()[1], &SSHSpec{Port: 22, User: "u", Password: "p"})))
	require.Equal(t, 1, calls)
	dao.WithEncryptionKeyFunc(func() (string, error) { return "", errors.New("no key") })
	require.Error(t, dao.SyncHostDeployments(context.Background(), withSSH(topologySpecs()[1], &SSHSpec{Port: 22, User: "u", Password: "p"})))
}

type recordingCloser struct{ hosts []string }

func (r *recordingCloser) CloseHostSessions(hostID string) { r.hosts = append(r.hosts, hostID) }

func TestServiceClosesSessionsAfterSyncAndDelete(t *testing.T) {
	dao := sshTopologyDAO(t)
	service := &ServiceImpl{db: dao.db, adminNodeID: "control"}
	closer := &recordingCloser{}
	service.SetSessionCloser(closer)
	ctx := context.Background()

	rsp, err := service.SyncHostDeployments(ctx, &pb.SyncHostDeploymentsReq{HostId: "overseas", Address: "compute.example.test", ComponentIds: []string{"trade", "access"}, Label: "海外机", SshPort: 22, SshUser: "u", SshAuthType: "password", SshSecret: "p"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	require.Equal(t, []string{"overseas"}, closer.hosts)

	bad, err := service.SyncHostDeployments(ctx, &pb.SyncHostDeploymentsReq{HostId: "overseas", Address: "compute.example.test", ComponentIds: []string{"trade", "access"}, SshPort: 22, SshUser: "u", SshAuthType: "password"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, bad.GetRetInfo().GetCode())
	require.Len(t, closer.hosts, 1, "失败的同步不关闭会话")

	list, err := service.ListHosts(ctx, &pb.ListDeploymentHostsReq{HostId: "overseas"})
	require.NoError(t, err)
	require.Len(t, list.GetHosts(), 1)
	require.Equal(t, "海外机", list.GetHosts()[0].GetLabel())
	require.EqualValues(t, 22, list.GetHosts()[0].GetSshPort())
	require.Equal(t, "u", list.GetHosts()[0].GetSshUser())
}
