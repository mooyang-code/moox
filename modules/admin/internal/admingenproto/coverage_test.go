// Package admingenproto 在 admin 主模块内执行 proto/admingen 覆盖测试。
// proto/admingen 为独立 go.mod，标准 ./... 不会执行其子模块测试，故在此补测。
package admingenproto

import (
	"context"
	"reflect"
	"testing"

	mooxpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/filter"
)

func exerciseMessage(t *testing.T, msg interface {
	Reset()
	String() string
	ProtoMessage()
}) {
	t.Helper()
	msg.Reset()
	_ = msg.String()
	msg.ProtoMessage()
}

func noopFilter(req interface{}) (filter.ServerChain, error) {
	return filter.ServerChain{filter.NoopServerFilter}, nil
}

func allProtoMessages() []proto.Message {
	return []proto.Message{
		&mooxpb.AppInfo{}, &mooxpb.ChangePasswordReq{}, &mooxpb.ChangePasswordRsp{},
		&mooxpb.CreateHostReq{Host: &mooxpb.SSHHost{}}, &mooxpb.CreateHostRsp{},
		&mooxpb.CreateSecretReq{Secret: &mooxpb.Secret{}}, &mooxpb.CreateSecretRsp{},
		&mooxpb.CreateServiceDeploymentReq{Deployment: &mooxpb.ServiceDeployment{}}, &mooxpb.CreateServiceDeploymentRsp{},
		&mooxpb.CreateSessionReq{}, &mooxpb.CreateSessionRsp{},
		&mooxpb.CreateSpaceReq{Space: &mooxpb.Space{}}, &mooxpb.CreateSpaceRsp{Space: &mooxpb.Space{}},
		&mooxpb.DNSRecord{IpList: []*mooxpb.IPInfo{{}}}, &mooxpb.DeleteHostReq{}, &mooxpb.DeleteHostRsp{},
		&mooxpb.DeleteSecretReq{}, &mooxpb.DeleteSecretRsp{},
		&mooxpb.DeleteServiceDeploymentReq{}, &mooxpb.DeleteServiceDeploymentRsp{},
		&mooxpb.DisconnectSessionReq{}, &mooxpb.DisconnectSessionRsp{},
		&mooxpb.ForceDisconnectReq{}, &mooxpb.ForceDisconnectRsp{},
		&mooxpb.GetChangePasswordSaltReq{}, &mooxpb.GetChangePasswordSaltRsp{},
		&mooxpb.GetDNSRecordReq{}, &mooxpb.GetDNSRecordRsp{Record: &mooxpb.DNSRecord{}},
		&mooxpb.GetHostReq{}, &mooxpb.GetHostRsp{Host: &mooxpb.SSHHost{}},
		&mooxpb.GetLoginSaltReq{}, &mooxpb.GetLoginSaltRsp{},
		&mooxpb.GetOnlineSessionsReq{}, &mooxpb.GetOnlineSessionsRsp{Sessions: []*mooxpb.SessionInfo{{}}},
		&mooxpb.GetSecretReq{}, &mooxpb.GetSecretRsp{Secret: &mooxpb.Secret{}},
		&mooxpb.GetServiceDeploymentReq{}, &mooxpb.GetServiceDeploymentRsp{Deployment: &mooxpb.ServiceDeployment{}},
		&mooxpb.GetUserInfoReq{}, &mooxpb.GetUserInfoRsp{UserInfo: &mooxpb.UserInfo{}},
		&mooxpb.IPInfo{}, &mooxpb.ListActiveServiceDeploymentsReq{},
		&mooxpb.ListActiveServiceDeploymentsRsp{Deployments: []*mooxpb.ServiceDeployment{{}}},
		&mooxpb.ListDNSRecordsReq{}, &mooxpb.ListDNSRecordsRsp{Records: []*mooxpb.DNSRecord{{}}},
		&mooxpb.ListHostsReq{}, &mooxpb.ListHostsRsp{Hosts: []*mooxpb.SSHHost{{}}},
		&mooxpb.ListSecretsReq{}, &mooxpb.ListSecretsRsp{Secrets: []*mooxpb.Secret{{}}},
		&mooxpb.ListServiceDeploymentsReq{}, &mooxpb.ListServiceDeploymentsRsp{Deployments: []*mooxpb.ServiceDeployment{{}}},
		&mooxpb.ListSpaceMembersReq{}, &mooxpb.ListSpaceMembersRsp{Members: []*mooxpb.SpaceMember{{}}},
		&mooxpb.ListSpacesReq{}, &mooxpb.ListSpacesRsp{Spaces: []*mooxpb.Space{{}}},
		&mooxpb.LoginReq{}, &mooxpb.LoginRsp{RetInfo: &mooxpb.RetInfo{}, UserInfo: &mooxpb.UserInfo{}},
		&mooxpb.PathBreadcrumb{}, &mooxpb.RegisterReq{}, &mooxpb.RegisterRsp{UserInfo: &mooxpb.UserInfo{}},
		&mooxpb.ResizeWindowReq{}, &mooxpb.ResizeWindowRsp{},
		&mooxpb.RevealSecretReq{}, &mooxpb.RevealSecretRsp{Secret: &mooxpb.Secret{}},
		&mooxpb.SSHHost{}, &mooxpb.Secret{}, &mooxpb.ServiceDeployment{},
		&mooxpb.ServiceDeploymentEndpoint{}, &mooxpb.ServiceDeploymentWarning{},
		&mooxpb.SessionInfo{}, &mooxpb.SftpDeleteReq{}, &mooxpb.SftpDeleteRsp{},
		&mooxpb.SftpFileItem{}, &mooxpb.SftpListReq{}, &mooxpb.SftpListRsp{Files: []*mooxpb.SftpFileItem{{}}},
		&mooxpb.SftpMkdirReq{}, &mooxpb.SftpMkdirRsp{}, &mooxpb.Space{}, &mooxpb.SpaceMember{},
		&mooxpb.ToggleSecretStatusReq{}, &mooxpb.ToggleSecretStatusRsp{},
		&mooxpb.UpdateHostReq{Host: &mooxpb.SSHHost{}}, &mooxpb.UpdateHostRsp{},
		&mooxpb.UpdateSecretReq{}, &mooxpb.UpdateSecretRsp{},
		&mooxpb.UpdateServiceDeploymentReq{Deployment: &mooxpb.ServiceDeployment{}}, &mooxpb.UpdateServiceDeploymentRsp{},
		&mooxpb.UpdateSpaceReq{Space: &mooxpb.Space{}}, &mooxpb.UpdateSpaceRsp{},
		&mooxpb.UpdateUserInfoReq{}, &mooxpb.UpdateUserInfoRsp{UserInfo: &mooxpb.UserInfo{}},
		&mooxpb.UserInfo{},
	}
}

func invokeGetters(t *testing.T, msg proto.Message) {
	t.Helper()
	rv := reflect.ValueOf(msg)
	if rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	rt := rv.Type()
	for i := 0; i < rt.NumMethod(); i++ {
		m := rt.Method(i)
		if len(m.Name) < 3 || m.Name[:3] != "Get" {
			continue
		}
		if m.Type.NumIn() != 1 || m.Type.NumOut() == 0 {
			continue
		}
		m.Func.Call([]reflect.Value{rv})
	}
}

type authStub struct{}

func (s *authStub) Login(context.Context, *mooxpb.LoginReq) (*mooxpb.LoginRsp, error) {
	return &mooxpb.LoginRsp{RetInfo: &mooxpb.RetInfo{Code: mooxpb.ErrorCode_SUCCESS}}, nil
}
func (s *authStub) Register(context.Context, *mooxpb.RegisterReq) (*mooxpb.RegisterRsp, error) {
	return &mooxpb.RegisterRsp{UserId: "u1"}, nil
}
func (s *authStub) GetLoginSalt(context.Context, *mooxpb.GetLoginSaltReq) (*mooxpb.GetLoginSaltRsp, error) {
	return &mooxpb.GetLoginSaltRsp{Salt: "salt"}, nil
}
func (s *authStub) GetUserInfo(context.Context, *mooxpb.GetUserInfoReq) (*mooxpb.GetUserInfoRsp, error) {
	return &mooxpb.GetUserInfoRsp{UserInfo: &mooxpb.UserInfo{UserId: "u1"}}, nil
}
func (s *authStub) UpdateUserInfo(context.Context, *mooxpb.UpdateUserInfoReq) (*mooxpb.UpdateUserInfoRsp, error) {
	return &mooxpb.UpdateUserInfoRsp{}, nil
}
func (s *authStub) GetChangePasswordSalt(context.Context, *mooxpb.GetChangePasswordSaltReq) (*mooxpb.GetChangePasswordSaltRsp, error) {
	return &mooxpb.GetChangePasswordSaltRsp{Salt: "salt"}, nil
}
func (s *authStub) ChangePassword(context.Context, *mooxpb.ChangePasswordReq) (*mooxpb.ChangePasswordRsp, error) {
	return &mooxpb.ChangePasswordRsp{}, nil
}

type spaceStub struct{}

func (s *spaceStub) CreateSpace(context.Context, *mooxpb.CreateSpaceReq) (*mooxpb.CreateSpaceRsp, error) {
	return &mooxpb.CreateSpaceRsp{Space: &mooxpb.Space{SpaceId: "sp"}}, nil
}
func (s *spaceStub) UpdateSpace(context.Context, *mooxpb.UpdateSpaceReq) (*mooxpb.UpdateSpaceRsp, error) {
	return &mooxpb.UpdateSpaceRsp{}, nil
}
func (s *spaceStub) ListSpaces(context.Context, *mooxpb.ListSpacesReq) (*mooxpb.ListSpacesRsp, error) {
	return &mooxpb.ListSpacesRsp{}, nil
}
func (s *spaceStub) ListSpaceMembers(context.Context, *mooxpb.ListSpaceMembersReq) (*mooxpb.ListSpaceMembersRsp, error) {
	return &mooxpb.ListSpaceMembersRsp{}, nil
}

type dnsStub struct{}

func (s *dnsStub) ListDNSRecords(context.Context, *mooxpb.ListDNSRecordsReq) (*mooxpb.ListDNSRecordsRsp, error) {
	return &mooxpb.ListDNSRecordsRsp{}, nil
}
func (s *dnsStub) GetDNSRecord(context.Context, *mooxpb.GetDNSRecordReq) (*mooxpb.GetDNSRecordRsp, error) {
	return &mooxpb.GetDNSRecordRsp{Record: &mooxpb.DNSRecord{Domain: "a.com"}}, nil
}

type sshStub struct{}

func (s *sshStub) ListHosts(context.Context, *mooxpb.ListHostsReq) (*mooxpb.ListHostsRsp, error) {
	return &mooxpb.ListHostsRsp{}, nil
}
func (s *sshStub) CreateHost(context.Context, *mooxpb.CreateHostReq) (*mooxpb.CreateHostRsp, error) {
	return &mooxpb.CreateHostRsp{}, nil
}
func (s *sshStub) UpdateHost(context.Context, *mooxpb.UpdateHostReq) (*mooxpb.UpdateHostRsp, error) {
	return &mooxpb.UpdateHostRsp{}, nil
}
func (s *sshStub) DeleteHost(context.Context, *mooxpb.DeleteHostReq) (*mooxpb.DeleteHostRsp, error) {
	return &mooxpb.DeleteHostRsp{}, nil
}
func (s *sshStub) GetHost(context.Context, *mooxpb.GetHostReq) (*mooxpb.GetHostRsp, error) {
	return &mooxpb.GetHostRsp{}, nil
}
func (s *sshStub) CreateSession(context.Context, *mooxpb.CreateSessionReq) (*mooxpb.CreateSessionRsp, error) {
	return &mooxpb.CreateSessionRsp{SessionId: "sid"}, nil
}
func (s *sshStub) DisconnectSession(context.Context, *mooxpb.DisconnectSessionReq) (*mooxpb.DisconnectSessionRsp, error) {
	return &mooxpb.DisconnectSessionRsp{}, nil
}
func (s *sshStub) ResizeWindow(context.Context, *mooxpb.ResizeWindowReq) (*mooxpb.ResizeWindowRsp, error) {
	return &mooxpb.ResizeWindowRsp{}, nil
}
func (s *sshStub) SftpList(context.Context, *mooxpb.SftpListReq) (*mooxpb.SftpListRsp, error) {
	return &mooxpb.SftpListRsp{}, nil
}
func (s *sshStub) SftpMkdir(context.Context, *mooxpb.SftpMkdirReq) (*mooxpb.SftpMkdirRsp, error) {
	return &mooxpb.SftpMkdirRsp{}, nil
}
func (s *sshStub) SftpDelete(context.Context, *mooxpb.SftpDeleteReq) (*mooxpb.SftpDeleteRsp, error) {
	return &mooxpb.SftpDeleteRsp{}, nil
}
func (s *sshStub) GetOnlineSessions(context.Context, *mooxpb.GetOnlineSessionsReq) (*mooxpb.GetOnlineSessionsRsp, error) {
	return &mooxpb.GetOnlineSessionsRsp{}, nil
}
func (s *sshStub) ForceDisconnect(context.Context, *mooxpb.ForceDisconnectReq) (*mooxpb.ForceDisconnectRsp, error) {
	return &mooxpb.ForceDisconnectRsp{}, nil
}

type secretStub struct{}

func (s *secretStub) ListSecrets(context.Context, *mooxpb.ListSecretsReq) (*mooxpb.ListSecretsRsp, error) {
	return &mooxpb.ListSecretsRsp{}, nil
}
func (s *secretStub) GetSecret(context.Context, *mooxpb.GetSecretReq) (*mooxpb.GetSecretRsp, error) {
	return &mooxpb.GetSecretRsp{}, nil
}
func (s *secretStub) RevealSecret(context.Context, *mooxpb.RevealSecretReq) (*mooxpb.RevealSecretRsp, error) {
	return &mooxpb.RevealSecretRsp{Secret: &mooxpb.Secret{Name: "k"}}, nil
}
func (s *secretStub) CreateSecret(context.Context, *mooxpb.CreateSecretReq) (*mooxpb.CreateSecretRsp, error) {
	return &mooxpb.CreateSecretRsp{}, nil
}
func (s *secretStub) UpdateSecret(context.Context, *mooxpb.UpdateSecretReq) (*mooxpb.UpdateSecretRsp, error) {
	return &mooxpb.UpdateSecretRsp{}, nil
}
func (s *secretStub) DeleteSecret(context.Context, *mooxpb.DeleteSecretReq) (*mooxpb.DeleteSecretRsp, error) {
	return &mooxpb.DeleteSecretRsp{}, nil
}
func (s *secretStub) ToggleSecretStatus(context.Context, *mooxpb.ToggleSecretStatusReq) (*mooxpb.ToggleSecretStatusRsp, error) {
	return &mooxpb.ToggleSecretStatusRsp{}, nil
}

type sysDeployStub struct{}

func (s *sysDeployStub) ListServiceDeployments(context.Context, *mooxpb.ListServiceDeploymentsReq) (*mooxpb.ListServiceDeploymentsRsp, error) {
	return &mooxpb.ListServiceDeploymentsRsp{}, nil
}
func (s *sysDeployStub) GetServiceDeployment(context.Context, *mooxpb.GetServiceDeploymentReq) (*mooxpb.GetServiceDeploymentRsp, error) {
	return &mooxpb.GetServiceDeploymentRsp{}, nil
}
func (s *sysDeployStub) CreateServiceDeployment(context.Context, *mooxpb.CreateServiceDeploymentReq) (*mooxpb.CreateServiceDeploymentRsp, error) {
	return &mooxpb.CreateServiceDeploymentRsp{}, nil
}
func (s *sysDeployStub) UpdateServiceDeployment(context.Context, *mooxpb.UpdateServiceDeploymentReq) (*mooxpb.UpdateServiceDeploymentRsp, error) {
	return &mooxpb.UpdateServiceDeploymentRsp{}, nil
}
func (s *sysDeployStub) DeleteServiceDeployment(context.Context, *mooxpb.DeleteServiceDeploymentReq) (*mooxpb.DeleteServiceDeploymentRsp, error) {
	return &mooxpb.DeleteServiceDeploymentRsp{}, nil
}
func (s *sysDeployStub) ListActiveServiceDeployments(context.Context, *mooxpb.ListActiveServiceDeploymentsReq) (*mooxpb.ListActiveServiceDeploymentsRsp, error) {
	return &mooxpb.ListActiveServiceDeploymentsRsp{Deployments: []*mooxpb.ServiceDeployment{{ServiceName: "svc"}}}, nil
}

type fakeService struct{ registered bool }

func (f *fakeService) Register(serviceDesc interface{}, serviceImpl interface{}) error {
	f.registered = true
	return nil
}
func (f *fakeService) Serve() error              { return nil }
func (f *fakeService) Close(chan struct{}) error { return nil }

func TestProtoMessages_ReflectAndGetters(t *testing.T) {
	for _, msg := range allProtoMessages() {
		t.Run(reflect.TypeOf(msg).Elem().Name(), func(t *testing.T) {
			exerciseMessage(t, msg.(interface {
				Reset()
				String() string
				ProtoMessage()
			}))
			pr := msg.ProtoReflect()
			desc := pr.Descriptor()
			require.NotNil(t, desc)
			fields := desc.Fields()
			for i := 0; i < fields.Len(); i++ {
				fd := fields.Get(i)
				_ = pr.Get(fd)
			}
			invokeGetters(t, msg)
		})
	}
}

func TestAllServiceHandlers_ShouldDispatch(t *testing.T) {
	ctx := context.Background()
	auth := &authStub{}
	space := &spaceStub{}
	dns := &dnsStub{}
	ssh := &sshStub{}
	secret := &secretStub{}
	sys := &sysDeployStub{}

	handlers := []struct {
		name string
		run  func() (interface{}, error)
	}{
		{"AuthLogin", func() (interface{}, error) { return mooxpb.AuthService_Login_Handler(auth, ctx, noopFilter) }},
		{"AuthRegister", func() (interface{}, error) { return mooxpb.AuthService_Register_Handler(auth, ctx, noopFilter) }},
		{"AuthGetLoginSalt", func() (interface{}, error) { return mooxpb.AuthService_GetLoginSalt_Handler(auth, ctx, noopFilter) }},
		{"AuthGetUserInfo", func() (interface{}, error) { return mooxpb.AuthService_GetUserInfo_Handler(auth, ctx, noopFilter) }},
		{"AuthUpdateUserInfo", func() (interface{}, error) { return mooxpb.AuthService_UpdateUserInfo_Handler(auth, ctx, noopFilter) }},
		{"AuthGetChangePasswordSalt", func() (interface{}, error) { return mooxpb.AuthService_GetChangePasswordSalt_Handler(auth, ctx, noopFilter) }},
		{"AuthChangePassword", func() (interface{}, error) { return mooxpb.AuthService_ChangePassword_Handler(auth, ctx, noopFilter) }},
		{"SpaceCreate", func() (interface{}, error) { return mooxpb.SpaceMgrService_CreateSpace_Handler(space, ctx, noopFilter) }},
		{"SpaceUpdate", func() (interface{}, error) { return mooxpb.SpaceMgrService_UpdateSpace_Handler(space, ctx, noopFilter) }},
		{"SpaceList", func() (interface{}, error) { return mooxpb.SpaceMgrService_ListSpaces_Handler(space, ctx, noopFilter) }},
		{"SpaceMembers", func() (interface{}, error) { return mooxpb.SpaceMgrService_ListSpaceMembers_Handler(space, ctx, noopFilter) }},
		{"DnsList", func() (interface{}, error) { return mooxpb.DnsService_ListDNSRecords_Handler(dns, ctx, noopFilter) }},
		{"DnsGet", func() (interface{}, error) { return mooxpb.DnsService_GetDNSRecord_Handler(dns, ctx, noopFilter) }},
		{"SshListHosts", func() (interface{}, error) { return mooxpb.SshService_ListHosts_Handler(ssh, ctx, noopFilter) }},
		{"SshCreateHost", func() (interface{}, error) { return mooxpb.SshService_CreateHost_Handler(ssh, ctx, noopFilter) }},
		{"SshUpdateHost", func() (interface{}, error) { return mooxpb.SshService_UpdateHost_Handler(ssh, ctx, noopFilter) }},
		{"SshDeleteHost", func() (interface{}, error) { return mooxpb.SshService_DeleteHost_Handler(ssh, ctx, noopFilter) }},
		{"SshGetHost", func() (interface{}, error) { return mooxpb.SshService_GetHost_Handler(ssh, ctx, noopFilter) }},
		{"SshCreateSession", func() (interface{}, error) { return mooxpb.SshService_CreateSession_Handler(ssh, ctx, noopFilter) }},
		{"SshDisconnect", func() (interface{}, error) { return mooxpb.SshService_DisconnectSession_Handler(ssh, ctx, noopFilter) }},
		{"SshResize", func() (interface{}, error) { return mooxpb.SshService_ResizeWindow_Handler(ssh, ctx, noopFilter) }},
		{"SshSftpList", func() (interface{}, error) { return mooxpb.SshService_SftpList_Handler(ssh, ctx, noopFilter) }},
		{"SshSftpMkdir", func() (interface{}, error) { return mooxpb.SshService_SftpMkdir_Handler(ssh, ctx, noopFilter) }},
		{"SshSftpDelete", func() (interface{}, error) { return mooxpb.SshService_SftpDelete_Handler(ssh, ctx, noopFilter) }},
		{"SshOnline", func() (interface{}, error) { return mooxpb.SshService_GetOnlineSessions_Handler(ssh, ctx, noopFilter) }},
		{"SshForceDisconnect", func() (interface{}, error) { return mooxpb.SshService_ForceDisconnect_Handler(ssh, ctx, noopFilter) }},
		{"SecretList", func() (interface{}, error) { return mooxpb.SecretMgrService_ListSecrets_Handler(secret, ctx, noopFilter) }},
		{"SecretGet", func() (interface{}, error) { return mooxpb.SecretMgrService_GetSecret_Handler(secret, ctx, noopFilter) }},
		{"SecretReveal", func() (interface{}, error) { return mooxpb.SecretMgrService_RevealSecret_Handler(secret, ctx, noopFilter) }},
		{"SecretCreate", func() (interface{}, error) { return mooxpb.SecretMgrService_CreateSecret_Handler(secret, ctx, noopFilter) }},
		{"SecretUpdate", func() (interface{}, error) { return mooxpb.SecretMgrService_UpdateSecret_Handler(secret, ctx, noopFilter) }},
		{"SecretDelete", func() (interface{}, error) { return mooxpb.SecretMgrService_DeleteSecret_Handler(secret, ctx, noopFilter) }},
		{"SecretToggle", func() (interface{}, error) { return mooxpb.SecretMgrService_ToggleSecretStatus_Handler(secret, ctx, noopFilter) }},
		{"SysList", func() (interface{}, error) { return mooxpb.SysDeployService_ListServiceDeployments_Handler(sys, ctx, noopFilter) }},
		{"SysGet", func() (interface{}, error) { return mooxpb.SysDeployService_GetServiceDeployment_Handler(sys, ctx, noopFilter) }},
		{"SysCreate", func() (interface{}, error) { return mooxpb.SysDeployService_CreateServiceDeployment_Handler(sys, ctx, noopFilter) }},
		{"SysUpdate", func() (interface{}, error) { return mooxpb.SysDeployService_UpdateServiceDeployment_Handler(sys, ctx, noopFilter) }},
		{"SysDelete", func() (interface{}, error) { return mooxpb.SysDeployService_DeleteServiceDeployment_Handler(sys, ctx, noopFilter) }},
		{"SysListActive", func() (interface{}, error) { return mooxpb.SysDeployService_ListActiveServiceDeployments_Handler(sys, ctx, noopFilter) }},
	}
	for _, h := range handlers {
		t.Run(h.name, func(t *testing.T) {
			_, err := h.run()
			require.NoError(t, err)
		})
	}
}

func TestUnimplementedAndClientProxies(t *testing.T) {
	ctx := context.Background()
	_, err := (&mooxpb.UnimplementedAuth{}).Login(ctx, &mooxpb.LoginReq{})
	assert.Error(t, err)
	_, err = (&mooxpb.UnimplementedSpaceMgr{}).CreateSpace(ctx, &mooxpb.CreateSpaceReq{})
	assert.Error(t, err)
	_, err = (&mooxpb.UnimplementedDns{}).ListDNSRecords(ctx, &mooxpb.ListDNSRecordsReq{})
	assert.Error(t, err)
	_, err = (&mooxpb.UnimplementedSsh{}).ListHosts(ctx, &mooxpb.ListHostsReq{})
	assert.Error(t, err)
	_, err = (&mooxpb.UnimplementedSecretMgr{}).ListSecrets(ctx, &mooxpb.ListSecretsReq{})
	assert.Error(t, err)
	_, err = (&mooxpb.UnimplementedSysDeploy{}).ListServiceDeployments(ctx, &mooxpb.ListServiceDeploymentsReq{})
	assert.Error(t, err)

	auth := mooxpb.NewAuthClientProxy()
	space := mooxpb.NewSpaceMgrClientProxy()
	dns := mooxpb.NewDnsClientProxy()
	ssh := mooxpb.NewSshClientProxy()
	secret := mooxpb.NewSecretMgrClientProxy()
	sys := mooxpb.NewSysDeployClientProxy()

	calls := []func() error{
		func() error { _, err := auth.Login(ctx, &mooxpb.LoginReq{}); return err },
		func() error { _, err := auth.Register(ctx, &mooxpb.RegisterReq{}); return err },
		func() error { _, err := auth.GetLoginSalt(ctx, &mooxpb.GetLoginSaltReq{}); return err },
		func() error { _, err := auth.GetUserInfo(ctx, &mooxpb.GetUserInfoReq{}); return err },
		func() error { _, err := auth.UpdateUserInfo(ctx, &mooxpb.UpdateUserInfoReq{}); return err },
		func() error { _, err := auth.GetChangePasswordSalt(ctx, &mooxpb.GetChangePasswordSaltReq{}); return err },
		func() error { _, err := auth.ChangePassword(ctx, &mooxpb.ChangePasswordReq{}); return err },
		func() error { _, err := space.CreateSpace(ctx, &mooxpb.CreateSpaceReq{}); return err },
		func() error { _, err := space.UpdateSpace(ctx, &mooxpb.UpdateSpaceReq{}); return err },
		func() error { _, err := space.ListSpaces(ctx, &mooxpb.ListSpacesReq{}); return err },
		func() error { _, err := space.ListSpaceMembers(ctx, &mooxpb.ListSpaceMembersReq{}); return err },
		func() error { _, err := dns.ListDNSRecords(ctx, &mooxpb.ListDNSRecordsReq{}); return err },
		func() error { _, err := dns.GetDNSRecord(ctx, &mooxpb.GetDNSRecordReq{}); return err },
		func() error { _, err := ssh.ListHosts(ctx, &mooxpb.ListHostsReq{}); return err },
		func() error { _, err := ssh.CreateHost(ctx, &mooxpb.CreateHostReq{}); return err },
		func() error { _, err := ssh.UpdateHost(ctx, &mooxpb.UpdateHostReq{}); return err },
		func() error { _, err := ssh.DeleteHost(ctx, &mooxpb.DeleteHostReq{}); return err },
		func() error { _, err := ssh.GetHost(ctx, &mooxpb.GetHostReq{}); return err },
		func() error { _, err := ssh.CreateSession(ctx, &mooxpb.CreateSessionReq{}); return err },
		func() error { _, err := ssh.DisconnectSession(ctx, &mooxpb.DisconnectSessionReq{}); return err },
		func() error { _, err := ssh.ResizeWindow(ctx, &mooxpb.ResizeWindowReq{}); return err },
		func() error { _, err := ssh.SftpList(ctx, &mooxpb.SftpListReq{}); return err },
		func() error { _, err := ssh.SftpMkdir(ctx, &mooxpb.SftpMkdirReq{}); return err },
		func() error { _, err := ssh.SftpDelete(ctx, &mooxpb.SftpDeleteReq{}); return err },
		func() error { _, err := ssh.GetOnlineSessions(ctx, &mooxpb.GetOnlineSessionsReq{}); return err },
		func() error { _, err := ssh.ForceDisconnect(ctx, &mooxpb.ForceDisconnectReq{}); return err },
		func() error { _, err := secret.ListSecrets(ctx, &mooxpb.ListSecretsReq{}); return err },
		func() error { _, err := secret.GetSecret(ctx, &mooxpb.GetSecretReq{}); return err },
		func() error { _, err := secret.RevealSecret(ctx, &mooxpb.RevealSecretReq{}); return err },
		func() error { _, err := secret.CreateSecret(ctx, &mooxpb.CreateSecretReq{}); return err },
		func() error { _, err := secret.UpdateSecret(ctx, &mooxpb.UpdateSecretReq{}); return err },
		func() error { _, err := secret.DeleteSecret(ctx, &mooxpb.DeleteSecretReq{}); return err },
		func() error { _, err := secret.ToggleSecretStatus(ctx, &mooxpb.ToggleSecretStatusReq{}); return err },
		func() error { _, err := sys.ListServiceDeployments(ctx, &mooxpb.ListServiceDeploymentsReq{}); return err },
		func() error { _, err := sys.GetServiceDeployment(ctx, &mooxpb.GetServiceDeploymentReq{}); return err },
		func() error { _, err := sys.CreateServiceDeployment(ctx, &mooxpb.CreateServiceDeploymentReq{}); return err },
		func() error { _, err := sys.UpdateServiceDeployment(ctx, &mooxpb.UpdateServiceDeploymentReq{}); return err },
		func() error { _, err := sys.DeleteServiceDeployment(ctx, &mooxpb.DeleteServiceDeploymentReq{}); return err },
		func() error { _, err := sys.ListActiveServiceDeployments(ctx, &mooxpb.ListActiveServiceDeploymentsReq{}); return err },
	}
	for i, call := range calls {
		assert.Error(t, call(), "client call %d should fail without backend", i)
	}

	s := &fakeService{}
	require.NotPanics(t, func() {
		mooxpb.RegisterAuthService(s, &authStub{})
		mooxpb.RegisterSpaceMgrService(s, &spaceStub{})
		mooxpb.RegisterDnsService(s, &dnsStub{})
		mooxpb.RegisterSshService(s, &sshStub{})
		mooxpb.RegisterSecretMgrService(s, &secretStub{})
		mooxpb.RegisterSysDeployService(s, &sysDeployStub{})
	})
	assert.True(t, s.registered)
	assert.NotEmpty(t, mooxpb.AuthServer_ServiceDesc.ServiceName)
}

func TestNilGetters_ShouldReturnZeroValues(t *testing.T) {
	// 调用各消息类型 nil 接收者上的全部 Get* 方法，覆盖 proto 生成代码的零值分支。
	nilMsgs := []proto.Message{
		(*mooxpb.AppInfo)(nil), (*mooxpb.ChangePasswordReq)(nil), (*mooxpb.ChangePasswordRsp)(nil),
		(*mooxpb.CreateHostReq)(nil), (*mooxpb.CreateHostRsp)(nil),
		(*mooxpb.CreateSecretReq)(nil), (*mooxpb.CreateSecretRsp)(nil),
		(*mooxpb.CreateServiceDeploymentReq)(nil), (*mooxpb.CreateServiceDeploymentRsp)(nil),
		(*mooxpb.CreateSessionReq)(nil), (*mooxpb.CreateSessionRsp)(nil),
		(*mooxpb.CreateSpaceReq)(nil), (*mooxpb.CreateSpaceRsp)(nil),
		(*mooxpb.DNSRecord)(nil), (*mooxpb.DeleteHostReq)(nil), (*mooxpb.DeleteHostRsp)(nil),
		(*mooxpb.DeleteSecretReq)(nil), (*mooxpb.DeleteSecretRsp)(nil),
		(*mooxpb.DeleteServiceDeploymentReq)(nil), (*mooxpb.DeleteServiceDeploymentRsp)(nil),
		(*mooxpb.DisconnectSessionReq)(nil), (*mooxpb.DisconnectSessionRsp)(nil),
		(*mooxpb.ForceDisconnectReq)(nil), (*mooxpb.ForceDisconnectRsp)(nil),
		(*mooxpb.GetChangePasswordSaltReq)(nil), (*mooxpb.GetChangePasswordSaltRsp)(nil),
		(*mooxpb.GetDNSRecordReq)(nil), (*mooxpb.GetDNSRecordRsp)(nil),
		(*mooxpb.GetHostReq)(nil), (*mooxpb.GetHostRsp)(nil),
		(*mooxpb.GetLoginSaltReq)(nil), (*mooxpb.GetLoginSaltRsp)(nil),
		(*mooxpb.GetOnlineSessionsReq)(nil), (*mooxpb.GetOnlineSessionsRsp)(nil),
		(*mooxpb.GetSecretReq)(nil), (*mooxpb.GetSecretRsp)(nil),
		(*mooxpb.GetServiceDeploymentReq)(nil), (*mooxpb.GetServiceDeploymentRsp)(nil),
		(*mooxpb.GetUserInfoReq)(nil), (*mooxpb.GetUserInfoRsp)(nil),
		(*mooxpb.IPInfo)(nil), (*mooxpb.ListActiveServiceDeploymentsReq)(nil),
		(*mooxpb.ListActiveServiceDeploymentsRsp)(nil),
		(*mooxpb.ListDNSRecordsReq)(nil), (*mooxpb.ListDNSRecordsRsp)(nil),
		(*mooxpb.ListHostsReq)(nil), (*mooxpb.ListHostsRsp)(nil),
		(*mooxpb.ListSecretsReq)(nil), (*mooxpb.ListSecretsRsp)(nil),
		(*mooxpb.ListServiceDeploymentsReq)(nil), (*mooxpb.ListServiceDeploymentsRsp)(nil),
		(*mooxpb.ListSpaceMembersReq)(nil), (*mooxpb.ListSpaceMembersRsp)(nil),
		(*mooxpb.ListSpacesReq)(nil), (*mooxpb.ListSpacesRsp)(nil),
		(*mooxpb.LoginReq)(nil), (*mooxpb.LoginRsp)(nil),
		(*mooxpb.PathBreadcrumb)(nil), (*mooxpb.RegisterReq)(nil), (*mooxpb.RegisterRsp)(nil),
		(*mooxpb.ResizeWindowReq)(nil), (*mooxpb.ResizeWindowRsp)(nil),
		(*mooxpb.RevealSecretReq)(nil), (*mooxpb.RevealSecretRsp)(nil),
		(*mooxpb.SSHHost)(nil), (*mooxpb.Secret)(nil), (*mooxpb.ServiceDeployment)(nil),
		(*mooxpb.ServiceDeploymentEndpoint)(nil), (*mooxpb.ServiceDeploymentWarning)(nil),
		(*mooxpb.SessionInfo)(nil), (*mooxpb.SftpDeleteReq)(nil), (*mooxpb.SftpDeleteRsp)(nil),
		(*mooxpb.SftpFileItem)(nil), (*mooxpb.SftpListReq)(nil), (*mooxpb.SftpListRsp)(nil),
		(*mooxpb.SftpMkdirReq)(nil), (*mooxpb.SftpMkdirRsp)(nil),
		(*mooxpb.Space)(nil), (*mooxpb.SpaceMember)(nil),
		(*mooxpb.ToggleSecretStatusReq)(nil), (*mooxpb.ToggleSecretStatusRsp)(nil),
		(*mooxpb.UpdateHostReq)(nil), (*mooxpb.UpdateHostRsp)(nil),
		(*mooxpb.UpdateSecretReq)(nil), (*mooxpb.UpdateSecretRsp)(nil),
		(*mooxpb.UpdateServiceDeploymentReq)(nil), (*mooxpb.UpdateServiceDeploymentRsp)(nil),
		(*mooxpb.UpdateSpaceReq)(nil), (*mooxpb.UpdateSpaceRsp)(nil),
		(*mooxpb.UpdateUserInfoReq)(nil), (*mooxpb.UpdateUserInfoRsp)(nil),
		(*mooxpb.UserInfo)(nil),
	}
	for _, msg := range nilMsgs {
		rv := reflect.ValueOf(msg)
		rt := rv.Type()
		for i := 0; i < rt.NumMethod(); i++ {
			m := rt.Method(i)
			if len(m.Name) < 3 || m.Name[:3] != "Get" {
				continue
			}
			if m.Type.NumIn() != 1 || m.Type.NumOut() == 0 {
				continue
			}
			require.NotPanics(t, func() {
				m.Func.Call([]reflect.Value{rv})
			}, "%s.%s", rt.String(), m.Name)
		}
	}
}
