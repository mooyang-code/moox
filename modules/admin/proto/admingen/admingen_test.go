package mooxpb

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestProtoMessages_ShouldSupportReflectionAndGetters(t *testing.T) {
	exerciseMessage(t, &AppInfo{})
	exerciseMessage(t, &ChangePasswordReq{Salt: "s", OldPasswordHash: "old", NewPasswordHash: "new"})
	exerciseMessage(t, &ChangePasswordRsp{RetInfo: &RetInfo{Code: ErrorCode_SUCCESS}})
	exerciseMessage(t, &CreateHostReq{Host: &SSHHost{Name: "dev"}})
	exerciseMessage(t, &CreateHostRsp{RetInfo: &RetInfo{}})
	exerciseMessage(t, &CreateSecretReq{Secret: &Secret{Name: "k"}})
	exerciseMessage(t, &CreateSecretRsp{})
	exerciseMessage(t, &CreateServiceDeploymentReq{Deployment: &ServiceDeployment{ServiceName: "svc"}})
	exerciseMessage(t, &CreateServiceDeploymentRsp{})
	exerciseMessage(t, &CreateSessionReq{HostId: 1})
	exerciseMessage(t, &CreateSessionRsp{SessionId: "sid"})
	exerciseMessage(t, &CreateSpaceReq{Space: &Space{Name: "default"}})
	exerciseMessage(t, &CreateSpaceRsp{Space: &Space{SpaceId: "sp-1"}})
	exerciseMessage(t, &DNSRecord{Domain: "example.com", IpList: []*IPInfo{{Ip: "1.1.1.1"}}})
	exerciseMessage(t, &DeleteHostReq{Id: 1})
	exerciseMessage(t, &DeleteHostRsp{})
	exerciseMessage(t, &DeleteSecretReq{SecretId: "1"})
	exerciseMessage(t, &DeleteSecretRsp{})
	exerciseMessage(t, &DeleteServiceDeploymentReq{ServiceName: "svc"})
	exerciseMessage(t, &DeleteServiceDeploymentRsp{})
	exerciseMessage(t, &DisconnectSessionReq{SessionId: "sid"})
	exerciseMessage(t, &DisconnectSessionRsp{})
	exerciseMessage(t, &ForceDisconnectReq{SessionId: "sid"})
	exerciseMessage(t, &ForceDisconnectRsp{})
	exerciseMessage(t, &GetChangePasswordSaltReq{})
	exerciseMessage(t, &GetChangePasswordSaltRsp{Salt: "salt"})
	exerciseMessage(t, &GetDNSRecordReq{Domain: "example.com"})
	exerciseMessage(t, &GetDNSRecordRsp{Record: &DNSRecord{Domain: "example.com"}})
	exerciseMessage(t, &GetHostReq{Id: 1})
	exerciseMessage(t, &GetHostRsp{Host: &SSHHost{Name: "dev"}})
	exerciseMessage(t, &GetLoginSaltReq{Username: "admin"})
	exerciseMessage(t, &GetLoginSaltRsp{Salt: "salt", Timestamp: 1})
	exerciseMessage(t, &GetOnlineSessionsReq{})
	exerciseMessage(t, &GetOnlineSessionsRsp{Sessions: []*SessionInfo{{SessionId: "s1"}}})
	exerciseMessage(t, &GetSecretReq{SecretId: "1"})
	exerciseMessage(t, &GetSecretRsp{Secret: &Secret{Name: "k"}})
	exerciseMessage(t, &GetServiceDeploymentReq{ServiceName: "svc"})
	exerciseMessage(t, &GetServiceDeploymentRsp{Deployment: &ServiceDeployment{ServiceName: "svc"}})
	exerciseMessage(t, &GetUserInfoReq{AccessToken: "token"})
	exerciseMessage(t, &GetUserInfoRsp{UserInfo: &UserInfo{UserId: "u1"}})
	exerciseMessage(t, &IPInfo{Ip: "1.1.1.1", Available: true})
	exerciseMessage(t, &ListActiveServiceDeploymentsReq{})
	exerciseMessage(t, &ListActiveServiceDeploymentsRsp{Deployments: []*ServiceDeployment{{ServiceName: "svc"}}})
	exerciseMessage(t, &ListDNSRecordsReq{})
	exerciseMessage(t, &ListDNSRecordsRsp{Records: []*DNSRecord{{Domain: "a.com"}}})
	exerciseMessage(t, &ListHostsReq{Keyword: "dev"})
	exerciseMessage(t, &ListHostsRsp{Hosts: []*SSHHost{{Name: "dev"}}})
	exerciseMessage(t, &ListSecretsReq{})
	exerciseMessage(t, &ListSecretsRsp{Secrets: []*Secret{{Name: "k"}}})
	exerciseMessage(t, &ListServiceDeploymentsReq{})
	exerciseMessage(t, &ListServiceDeploymentsRsp{Deployments: []*ServiceDeployment{{ServiceName: "svc"}}})
	exerciseMessage(t, &ListSpaceMembersReq{SpaceId: "sp"})
	exerciseMessage(t, &ListSpaceMembersRsp{Members: []*SpaceMember{{UserId: "u1"}}})
	exerciseMessage(t, &ListSpacesReq{})
	exerciseMessage(t, &ListSpacesRsp{Spaces: []*Space{{Name: "default"}}})
	exerciseMessage(t, &LoginReq{Username: "admin"})
	exerciseMessage(t, &LoginRsp{AccessToken: "token"})
	exerciseMessage(t, &PathBreadcrumb{Name: "var", Dir: "/var"})
	exerciseMessage(t, &RegisterReq{Username: "new"})
	exerciseMessage(t, &RegisterRsp{UserId: "u1"})
	exerciseMessage(t, &ResizeWindowReq{SessionId: "sid", W: 80, H: 24})
	exerciseMessage(t, &ResizeWindowRsp{})
	exerciseMessage(t, &RevealSecretReq{SecretId: "1"})
	exerciseMessage(t, &RevealSecretRsp{Secret: &Secret{Name: "k"}})
	exerciseMessage(t, &SSHHost{Name: "dev", Address: "10.0.0.1", Port: 22})
	exerciseMessage(t, &Secret{Name: "k", SecretId: "sec-1"})
	exerciseMessage(t, &ServiceDeployment{ServiceName: "svc", Host: "127.0.0.1", Port: 8080})
	exerciseMessage(t, &ServiceDeploymentEndpoint{ServiceName: "svc", Host: "127.0.0.1", Port: 8080})
	exerciseMessage(t, &ServiceDeploymentWarning{ServiceName: "svc", Message: "warn"})
	exerciseMessage(t, &SessionInfo{SessionId: "sid", HostId: 1})
	exerciseMessage(t, &SftpDeleteReq{SessionId: "sid", Path: "/tmp"})
	exerciseMessage(t, &SftpDeleteRsp{})
	exerciseMessage(t, &SftpFileItem{Name: "a.txt", Path: "/a.txt"})
	exerciseMessage(t, &SftpListReq{SessionId: "sid", Path: "/"})
	exerciseMessage(t, &SftpListRsp{Files: []*SftpFileItem{{Name: "a.txt"}}})
	exerciseMessage(t, &SftpMkdirReq{SessionId: "sid", Path: "/tmp/new"})
	exerciseMessage(t, &SftpMkdirRsp{})
	exerciseMessage(t, &Space{SpaceId: "sp", Name: "default"})
	exerciseMessage(t, &SpaceMember{UserId: "u1", Role: "admin"})
	exerciseMessage(t, &ToggleSecretStatusReq{SecretId: "1", Status: "active"})
	exerciseMessage(t, &ToggleSecretStatusRsp{})
	exerciseMessage(t, &UpdateHostReq{Host: &SSHHost{Id: 1, Name: "dev"}})
	exerciseMessage(t, &UpdateHostRsp{})
	exerciseMessage(t, &UpdateSecretReq{SecretId: "1", Name: "k"})
	exerciseMessage(t, &UpdateSecretRsp{})
	exerciseMessage(t, &UpdateServiceDeploymentReq{Deployment: &ServiceDeployment{ServiceName: "svc"}})
	exerciseMessage(t, &UpdateServiceDeploymentRsp{})
	exerciseMessage(t, &UpdateSpaceReq{Space: &Space{SpaceId: "sp"}})
	exerciseMessage(t, &UpdateSpaceRsp{})
	exerciseMessage(t, &UpdateUserInfoReq{Nick: "nick"})
	exerciseMessage(t, &UpdateUserInfoRsp{UserInfo: &UserInfo{UserId: "u1"}})
	exerciseMessage(t, &UserInfo{UserId: "u1", Username: "admin"})

	login := &LoginRsp{RetInfo: &RetInfo{Code: ErrorCode_SUCCESS}, AccessToken: "tok", ExpiresIn: 3600}
	assert.Equal(t, ErrorCode_SUCCESS, login.GetRetInfo().GetCode())
	assert.Equal(t, "tok", login.GetAccessToken())
	_ = login.ProtoReflect()
}

func TestNilGetters_ShouldReturnZeroValues(t *testing.T) {
	var login *LoginRsp
	assert.Empty(t, login.GetAccessToken())
	var host *SSHHost
	assert.Empty(t, host.GetName())
	var deploy *ServiceDeployment
	assert.Empty(t, deploy.GetServiceName())
}

type authStub struct{}

func (s *authStub) Login(context.Context, *LoginReq) (*LoginRsp, error) {
	return &LoginRsp{RetInfo: &RetInfo{Code: ErrorCode_SUCCESS}}, nil
}
func (s *authStub) Register(context.Context, *RegisterReq) (*RegisterRsp, error) {
	return &RegisterRsp{UserId: "u1"}, nil
}
func (s *authStub) GetLoginSalt(context.Context, *GetLoginSaltReq) (*GetLoginSaltRsp, error) {
	return &GetLoginSaltRsp{Salt: "salt"}, nil
}
func (s *authStub) GetUserInfo(context.Context, *GetUserInfoReq) (*GetUserInfoRsp, error) {
	return &GetUserInfoRsp{UserInfo: &UserInfo{UserId: "u1"}}, nil
}
func (s *authStub) UpdateUserInfo(context.Context, *UpdateUserInfoReq) (*UpdateUserInfoRsp, error) {
	return &UpdateUserInfoRsp{}, nil
}
func (s *authStub) GetChangePasswordSalt(context.Context, *GetChangePasswordSaltReq) (*GetChangePasswordSaltRsp, error) {
	return &GetChangePasswordSaltRsp{Salt: "salt"}, nil
}
func (s *authStub) ChangePassword(context.Context, *ChangePasswordReq) (*ChangePasswordRsp, error) {
	return &ChangePasswordRsp{}, nil
}

func TestAuthServiceHandlers_ShouldDispatchRPCs(t *testing.T) {
	stub := &authStub{}
	ctx := context.Background()

	rsp, err := AuthService_Login_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.Equal(t, ErrorCode_SUCCESS, rsp.(*LoginRsp).GetRetInfo().GetCode())

	rsp, err = AuthService_GetUserInfo_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.Equal(t, "u1", rsp.(*GetUserInfoRsp).GetUserInfo().GetUserId())
}

type spaceStub struct{}

func (s *spaceStub) CreateSpace(context.Context, *CreateSpaceReq) (*CreateSpaceRsp, error) {
	return &CreateSpaceRsp{Space: &Space{SpaceId: "sp"}}, nil
}
func (s *spaceStub) UpdateSpace(context.Context, *UpdateSpaceReq) (*UpdateSpaceRsp, error) {
	return &UpdateSpaceRsp{}, nil
}
func (s *spaceStub) ListSpaces(context.Context, *ListSpacesReq) (*ListSpacesRsp, error) {
	return &ListSpacesRsp{}, nil
}
func (s *spaceStub) ListSpaceMembers(context.Context, *ListSpaceMembersReq) (*ListSpaceMembersRsp, error) {
	return &ListSpaceMembersRsp{}, nil
}

func TestSpaceMgrServiceHandlers_ShouldDispatchRPCs(t *testing.T) {
	stub := &spaceStub{}
	ctx := context.Background()
	rsp, err := SpaceMgrService_CreateSpace_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.Equal(t, "sp", rsp.(*CreateSpaceRsp).GetSpace().GetSpaceId())
}

type dnsStub struct{}

func (s *dnsStub) ListDNSRecords(context.Context, *ListDNSRecordsReq) (*ListDNSRecordsRsp, error) {
	return &ListDNSRecordsRsp{}, nil
}
func (s *dnsStub) GetDNSRecord(context.Context, *GetDNSRecordReq) (*GetDNSRecordRsp, error) {
	return &GetDNSRecordRsp{Record: &DNSRecord{Domain: "a.com"}}, nil
}

func TestDnsServiceHandlers_ShouldDispatchRPCs(t *testing.T) {
	stub := &dnsStub{}
	ctx := context.Background()
	rsp, err := DnsService_GetDNSRecord_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.Equal(t, "a.com", rsp.(*GetDNSRecordRsp).GetRecord().GetDomain())
}

type sshStub struct{}

func (s *sshStub) ListHosts(context.Context, *ListHostsReq) (*ListHostsRsp, error)       { return &ListHostsRsp{}, nil }
func (s *sshStub) CreateHost(context.Context, *CreateHostReq) (*CreateHostRsp, error)     { return &CreateHostRsp{}, nil }
func (s *sshStub) UpdateHost(context.Context, *UpdateHostReq) (*UpdateHostRsp, error)     { return &UpdateHostRsp{}, nil }
func (s *sshStub) DeleteHost(context.Context, *DeleteHostReq) (*DeleteHostRsp, error)     { return &DeleteHostRsp{}, nil }
func (s *sshStub) GetHost(context.Context, *GetHostReq) (*GetHostRsp, error)             { return &GetHostRsp{}, nil }
func (s *sshStub) CreateSession(context.Context, *CreateSessionReq) (*CreateSessionRsp, error) {
	return &CreateSessionRsp{SessionId: "sid"}, nil
}
func (s *sshStub) DisconnectSession(context.Context, *DisconnectSessionReq) (*DisconnectSessionRsp, error) {
	return &DisconnectSessionRsp{}, nil
}
func (s *sshStub) ResizeWindow(context.Context, *ResizeWindowReq) (*ResizeWindowRsp, error) { return &ResizeWindowRsp{}, nil }
func (s *sshStub) SftpList(context.Context, *SftpListReq) (*SftpListRsp, error)           { return &SftpListRsp{}, nil }
func (s *sshStub) SftpMkdir(context.Context, *SftpMkdirReq) (*SftpMkdirRsp, error)         { return &SftpMkdirRsp{}, nil }
func (s *sshStub) SftpDelete(context.Context, *SftpDeleteReq) (*SftpDeleteRsp, error)     { return &SftpDeleteRsp{}, nil }
func (s *sshStub) GetOnlineSessions(context.Context, *GetOnlineSessionsReq) (*GetOnlineSessionsRsp, error) {
	return &GetOnlineSessionsRsp{}, nil
}
func (s *sshStub) ForceDisconnect(context.Context, *ForceDisconnectReq) (*ForceDisconnectRsp, error) {
	return &ForceDisconnectRsp{}, nil
}

func TestSshServiceHandlers_ShouldDispatchRPCs(t *testing.T) {
	stub := &sshStub{}
	ctx := context.Background()
	rsp, err := SshService_CreateSession_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.Equal(t, "sid", rsp.(*CreateSessionRsp).GetSessionId())
}

type secretStub struct{}

func (s *secretStub) ListSecrets(context.Context, *ListSecretsReq) (*ListSecretsRsp, error) { return &ListSecretsRsp{}, nil }
func (s *secretStub) GetSecret(context.Context, *GetSecretReq) (*GetSecretRsp, error)       { return &GetSecretRsp{}, nil }
func (s *secretStub) RevealSecret(context.Context, *RevealSecretReq) (*RevealSecretRsp, error) {
	return &RevealSecretRsp{Secret: &Secret{Name: "k"}}, nil
}
func (s *secretStub) CreateSecret(context.Context, *CreateSecretReq) (*CreateSecretRsp, error) {
	return &CreateSecretRsp{}, nil
}
func (s *secretStub) UpdateSecret(context.Context, *UpdateSecretReq) (*UpdateSecretRsp, error) {
	return &UpdateSecretRsp{}, nil
}
func (s *secretStub) DeleteSecret(context.Context, *DeleteSecretReq) (*DeleteSecretRsp, error) {
	return &DeleteSecretRsp{}, nil
}
func (s *secretStub) ToggleSecretStatus(context.Context, *ToggleSecretStatusReq) (*ToggleSecretStatusRsp, error) {
	return &ToggleSecretStatusRsp{}, nil
}

func TestSecretMgrServiceHandlers_ShouldDispatchRPCs(t *testing.T) {
	stub := &secretStub{}
	ctx := context.Background()
	rsp, err := SecretMgrService_RevealSecret_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.Equal(t, "k", rsp.(*RevealSecretRsp).GetSecret().GetName())
}

type sysDeployStub struct{}

func (s *sysDeployStub) ListServiceDeployments(context.Context, *ListServiceDeploymentsReq) (*ListServiceDeploymentsRsp, error) {
	return &ListServiceDeploymentsRsp{}, nil
}
func (s *sysDeployStub) GetServiceDeployment(context.Context, *GetServiceDeploymentReq) (*GetServiceDeploymentRsp, error) {
	return &GetServiceDeploymentRsp{}, nil
}
func (s *sysDeployStub) CreateServiceDeployment(context.Context, *CreateServiceDeploymentReq) (*CreateServiceDeploymentRsp, error) {
	return &CreateServiceDeploymentRsp{}, nil
}
func (s *sysDeployStub) UpdateServiceDeployment(context.Context, *UpdateServiceDeploymentReq) (*UpdateServiceDeploymentRsp, error) {
	return &UpdateServiceDeploymentRsp{}, nil
}
func (s *sysDeployStub) DeleteServiceDeployment(context.Context, *DeleteServiceDeploymentReq) (*DeleteServiceDeploymentRsp, error) {
	return &DeleteServiceDeploymentRsp{}, nil
}
func (s *sysDeployStub) ListActiveServiceDeployments(context.Context, *ListActiveServiceDeploymentsReq) (*ListActiveServiceDeploymentsRsp, error) {
	return &ListActiveServiceDeploymentsRsp{Deployments: []*ServiceDeployment{{ServiceName: "svc"}}}, nil
}

func TestSysDeployServiceHandlers_ShouldDispatchRPCs(t *testing.T) {
	stub := &sysDeployStub{}
	ctx := context.Background()
	rsp, err := SysDeployService_ListActiveServiceDeployments_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.Len(t, rsp.(*ListActiveServiceDeploymentsRsp).GetDeployments(), 1)
}

func TestUnimplementedServices_ShouldReturnErrors(t *testing.T) {
	ctx := context.Background()
	_, err := (&UnimplementedAuth{}).Login(ctx, &LoginReq{})
	assert.Error(t, err)
	_, err = (&UnimplementedSsh{}).CreateSession(ctx, &CreateSessionReq{})
	assert.Error(t, err)
	_, err = (&UnimplementedSecretMgr{}).RevealSecret(ctx, &RevealSecretReq{})
	assert.Error(t, err)
	_, err = (&UnimplementedSysDeploy{}).ListServiceDeployments(ctx, &ListServiceDeploymentsReq{})
	assert.Error(t, err)
}

type fakeService struct{ registered bool }

func (f *fakeService) Register(serviceDesc interface{}, serviceImpl interface{}) error {
	f.registered = true
	return nil
}
func (f *fakeService) Serve() error                      { return nil }
func (f *fakeService) Close(chan struct{}) error         { return nil }

func TestRegisterServices_ShouldRegisterWithoutPanic(t *testing.T) {
	s := &fakeService{}
	require.NotPanics(t, func() {
		RegisterAuthService(s, &authStub{})
		RegisterSpaceMgrService(s, &spaceStub{})
		RegisterDnsService(s, &dnsStub{})
		RegisterSshService(s, &sshStub{})
		RegisterSecretMgrService(s, &secretStub{})
		RegisterSysDeployService(s, &sysDeployStub{})
	})
	assert.True(t, s.registered)
}

func TestServiceDescs_ShouldExposeMethods(t *testing.T) {
	assert.NotEmpty(t, AuthServer_ServiceDesc.ServiceName)
	assert.NotEmpty(t, SpaceMgrServer_ServiceDesc.Methods)
	assert.NotEmpty(t, DnsServer_ServiceDesc.Methods)
	assert.NotEmpty(t, SshServer_ServiceDesc.Methods)
	assert.NotEmpty(t, SecretMgrServer_ServiceDesc.Methods)
	assert.NotEmpty(t, SysDeployServer_ServiceDesc.Methods)
}
