package rpc

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/admin/internal/service/ssh/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/stretchr/testify/require"
)

type credentialService struct {
	fakeSSHService
	stored  model.SSHHost
	updated *model.SSHHost
}

func (c *credentialService) GetHost(context.Context, int) (*model.SSHHost, error) {
	host := c.stored
	return &host, nil
}
func (c *credentialService) UpdateHost(_ context.Context, host *model.SSHHost) error {
	c.updated = host
	return nil
}
func (c *credentialService) ListHosts(context.Context, string, int, int) ([]model.SSHHost, int64, error) {
	return []model.SSHHost{c.stored}, 1, nil
}

// 响应里不能带密码、私钥和私钥口令；保存时留空表示保持原值，不能把它们清空。
func TestHostCredentialsAreNeverReturnedAndKeptOnUpdate(t *testing.T) {
	backend := &credentialService{stored: model.SSHHost{ID: 7, Name: "prod", Password: "p4ss", CertData: "PRIVATE KEY", CertPwd: "phrase"}}
	svc := NewService(backend)
	list, err := svc.ListHosts(context.Background(), &pb.ListHostsReq{})
	require.NoError(t, err)
	got, err := svc.GetHost(context.Background(), &pb.GetHostReq{Id: 7})
	require.NoError(t, err)
	for _, host := range append(list.GetHosts(), got.GetHost()) {
		require.Empty(t, host.GetPassword())
		require.Empty(t, host.GetCertData())
		require.Empty(t, host.GetCertPwd())
	}
	_, err = svc.UpdateHost(context.Background(), &pb.UpdateHostReq{Host: &pb.SSHHost{Id: 7, Name: "renamed"}})
	require.NoError(t, err)
	require.Equal(t, "p4ss", backend.updated.Password)
	require.Equal(t, "PRIVATE KEY", backend.updated.CertData)
	require.Equal(t, "phrase", backend.updated.CertPwd)
	_, err = svc.UpdateHost(context.Background(), &pb.UpdateHostReq{Host: &pb.SSHHost{Id: 7, Password: "new"}})
	require.NoError(t, err)
	require.Equal(t, "new", backend.updated.Password)
}
