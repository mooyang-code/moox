package mooxpb

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAllServiceHandlers_ShouldDispatchWithoutError(t *testing.T) {
	ctx := context.Background()
	auth := &authStub{}
	space := &spaceStub{}
	dns := &dnsStub{}
	ssh := &sshStub{}
	secret := &secretStub{}
	sys := &sysDeployStub{}

	cases := []struct {
		name string
		run  func() (interface{}, error)
	}{
		{"AuthLogin", func() (interface{}, error) { return AuthService_Login_Handler(auth, ctx, noopFilter) }},
		{"AuthRegister", func() (interface{}, error) { return AuthService_Register_Handler(auth, ctx, noopFilter) }},
		{"AuthGetLoginSalt", func() (interface{}, error) { return AuthService_GetLoginSalt_Handler(auth, ctx, noopFilter) }},
		{"AuthGetUserInfo", func() (interface{}, error) { return AuthService_GetUserInfo_Handler(auth, ctx, noopFilter) }},
		{"AuthUpdateUserInfo", func() (interface{}, error) { return AuthService_UpdateUserInfo_Handler(auth, ctx, noopFilter) }},
		{"AuthGetChangePasswordSalt", func() (interface{}, error) { return AuthService_GetChangePasswordSalt_Handler(auth, ctx, noopFilter) }},
		{"AuthChangePassword", func() (interface{}, error) { return AuthService_ChangePassword_Handler(auth, ctx, noopFilter) }},
		{"SpaceCreate", func() (interface{}, error) { return SpaceMgrService_CreateSpace_Handler(space, ctx, noopFilter) }},
		{"SpaceUpdate", func() (interface{}, error) { return SpaceMgrService_UpdateSpace_Handler(space, ctx, noopFilter) }},
		{"SpaceList", func() (interface{}, error) { return SpaceMgrService_ListSpaces_Handler(space, ctx, noopFilter) }},
		{"SpaceMembers", func() (interface{}, error) { return SpaceMgrService_ListSpaceMembers_Handler(space, ctx, noopFilter) }},
		{"DnsList", func() (interface{}, error) { return DnsService_ListDNSRecords_Handler(dns, ctx, noopFilter) }},
		{"DnsGet", func() (interface{}, error) { return DnsService_GetDNSRecord_Handler(dns, ctx, noopFilter) }},
		{"SshListHosts", func() (interface{}, error) { return SshService_ListHosts_Handler(ssh, ctx, noopFilter) }},
		{"SshCreateHost", func() (interface{}, error) { return SshService_CreateHost_Handler(ssh, ctx, noopFilter) }},
		{"SshUpdateHost", func() (interface{}, error) { return SshService_UpdateHost_Handler(ssh, ctx, noopFilter) }},
		{"SshDeleteHost", func() (interface{}, error) { return SshService_DeleteHost_Handler(ssh, ctx, noopFilter) }},
		{"SshGetHost", func() (interface{}, error) { return SshService_GetHost_Handler(ssh, ctx, noopFilter) }},
		{"SshCreateSession", func() (interface{}, error) { return SshService_CreateSession_Handler(ssh, ctx, noopFilter) }},
		{"SshDisconnect", func() (interface{}, error) { return SshService_DisconnectSession_Handler(ssh, ctx, noopFilter) }},
		{"SshResize", func() (interface{}, error) { return SshService_ResizeWindow_Handler(ssh, ctx, noopFilter) }},
		{"SshSftpList", func() (interface{}, error) { return SshService_SftpList_Handler(ssh, ctx, noopFilter) }},
		{"SshSftpMkdir", func() (interface{}, error) { return SshService_SftpMkdir_Handler(ssh, ctx, noopFilter) }},
		{"SshSftpDelete", func() (interface{}, error) { return SshService_SftpDelete_Handler(ssh, ctx, noopFilter) }},
		{"SshOnline", func() (interface{}, error) { return SshService_GetOnlineSessions_Handler(ssh, ctx, noopFilter) }},
		{"SshForceDisconnect", func() (interface{}, error) { return SshService_ForceDisconnect_Handler(ssh, ctx, noopFilter) }},
		{"SecretList", func() (interface{}, error) { return SecretMgrService_ListSecrets_Handler(secret, ctx, noopFilter) }},
		{"SecretGet", func() (interface{}, error) { return SecretMgrService_GetSecret_Handler(secret, ctx, noopFilter) }},
		{"SecretReveal", func() (interface{}, error) { return SecretMgrService_RevealSecret_Handler(secret, ctx, noopFilter) }},
		{"SecretCreate", func() (interface{}, error) { return SecretMgrService_CreateSecret_Handler(secret, ctx, noopFilter) }},
		{"SecretUpdate", func() (interface{}, error) { return SecretMgrService_UpdateSecret_Handler(secret, ctx, noopFilter) }},
		{"SecretDelete", func() (interface{}, error) { return SecretMgrService_DeleteSecret_Handler(secret, ctx, noopFilter) }},
		{"SecretToggle", func() (interface{}, error) { return SecretMgrService_ToggleSecretStatus_Handler(secret, ctx, noopFilter) }},
		{"SysList", func() (interface{}, error) { return SysDeployService_ListServiceDeployments_Handler(sys, ctx, noopFilter) }},
		{"SysGet", func() (interface{}, error) { return SysDeployService_GetServiceDeployment_Handler(sys, ctx, noopFilter) }},
		{"SysCreate", func() (interface{}, error) { return SysDeployService_CreateServiceDeployment_Handler(sys, ctx, noopFilter) }},
		{"SysUpdate", func() (interface{}, error) { return SysDeployService_UpdateServiceDeployment_Handler(sys, ctx, noopFilter) }},
		{"SysDelete", func() (interface{}, error) { return SysDeployService_DeleteServiceDeployment_Handler(sys, ctx, noopFilter) }},
		{"SysListActive", func() (interface{}, error) { return SysDeployService_ListActiveServiceDeployments_Handler(sys, ctx, noopFilter) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.run()
			require.NoError(t, err)
		})
	}
}

func TestAllUnimplementedServices_ShouldReturnErrors(t *testing.T) {
	ctx := context.Background()
	auth := &UnimplementedAuth{}
	space := &UnimplementedSpaceMgr{}
	dns := &UnimplementedDns{}
	ssh := &UnimplementedSsh{}
	secret := &UnimplementedSecretMgr{}
	sys := &UnimplementedSysDeploy{}

	assert.Error(t, runErr(func() error { _, err := auth.Login(ctx, &LoginReq{}); return err }))
	assert.Error(t, runErr(func() error { _, err := auth.Register(ctx, &RegisterReq{}); return err }))
	assert.Error(t, runErr(func() error { _, err := space.CreateSpace(ctx, &CreateSpaceReq{}); return err }))
	assert.Error(t, runErr(func() error { _, err := dns.ListDNSRecords(ctx, &ListDNSRecordsReq{}); return err }))
	assert.Error(t, runErr(func() error { _, err := ssh.ListHosts(ctx, &ListHostsReq{}); return err }))
	assert.Error(t, runErr(func() error { _, err := secret.ListSecrets(ctx, &ListSecretsReq{}); return err }))
	assert.Error(t, runErr(func() error { _, err := sys.ListServiceDeployments(ctx, &ListServiceDeploymentsReq{}); return err }))
}

func runErr(fn func() error) error { return fn() }

func TestClientProxies_ShouldBuildRequests(t *testing.T) {
	ctx := context.Background()

	_, err := NewAuthClientProxy().Login(ctx, &LoginReq{})
	assert.Error(t, err)
	_, err = NewSpaceMgrClientProxy().ListSpaces(ctx, &ListSpacesReq{})
	assert.Error(t, err)
	_, err = NewDnsClientProxy().ListDNSRecords(ctx, &ListDNSRecordsReq{})
	assert.Error(t, err)
	_, err = NewSshClientProxy().ListHosts(ctx, &ListHostsReq{})
	assert.Error(t, err)
	_, err = NewSecretMgrClientProxy().ListSecrets(ctx, &ListSecretsReq{})
	assert.Error(t, err)
	_, err = NewSysDeployClientProxy().ListServiceDeployments(ctx, &ListServiceDeploymentsReq{})
	assert.Error(t, err)
}

func TestClientProxies_AllMethods_ShouldInvoke(t *testing.T) {
	ctx := context.Background()
	auth := NewAuthClientProxy()
	space := NewSpaceMgrClientProxy()
	dns := NewDnsClientProxy()
	ssh := NewSshClientProxy()
	secret := NewSecretMgrClientProxy()
	sys := NewSysDeployClientProxy()

	calls := []func() error{
		func() error { _, err := auth.Register(ctx, &RegisterReq{}); return err },
		func() error { _, err := auth.GetLoginSalt(ctx, &GetLoginSaltReq{}); return err },
		func() error { _, err := auth.GetUserInfo(ctx, &GetUserInfoReq{}); return err },
		func() error { _, err := auth.UpdateUserInfo(ctx, &UpdateUserInfoReq{}); return err },
		func() error { _, err := auth.GetChangePasswordSalt(ctx, &GetChangePasswordSaltReq{}); return err },
		func() error { _, err := auth.ChangePassword(ctx, &ChangePasswordReq{}); return err },
		func() error { _, err := space.CreateSpace(ctx, &CreateSpaceReq{}); return err },
		func() error { _, err := space.UpdateSpace(ctx, &UpdateSpaceReq{}); return err },
		func() error { _, err := space.ListSpaceMembers(ctx, &ListSpaceMembersReq{}); return err },
		func() error { _, err := dns.GetDNSRecord(ctx, &GetDNSRecordReq{}); return err },
		func() error { _, err := ssh.CreateHost(ctx, &CreateHostReq{}); return err },
		func() error { _, err := ssh.UpdateHost(ctx, &UpdateHostReq{}); return err },
		func() error { _, err := ssh.DeleteHost(ctx, &DeleteHostReq{}); return err },
		func() error { _, err := ssh.GetHost(ctx, &GetHostReq{}); return err },
		func() error { _, err := ssh.CreateSession(ctx, &CreateSessionReq{}); return err },
		func() error { _, err := ssh.DisconnectSession(ctx, &DisconnectSessionReq{}); return err },
		func() error { _, err := ssh.ResizeWindow(ctx, &ResizeWindowReq{}); return err },
		func() error { _, err := ssh.SftpList(ctx, &SftpListReq{}); return err },
		func() error { _, err := ssh.SftpMkdir(ctx, &SftpMkdirReq{}); return err },
		func() error { _, err := ssh.SftpDelete(ctx, &SftpDeleteReq{}); return err },
		func() error { _, err := ssh.GetOnlineSessions(ctx, &GetOnlineSessionsReq{}); return err },
		func() error { _, err := ssh.ForceDisconnect(ctx, &ForceDisconnectReq{}); return err },
		func() error { _, err := secret.GetSecret(ctx, &GetSecretReq{}); return err },
		func() error { _, err := secret.RevealSecret(ctx, &RevealSecretReq{}); return err },
		func() error { _, err := secret.CreateSecret(ctx, &CreateSecretReq{}); return err },
		func() error { _, err := secret.UpdateSecret(ctx, &UpdateSecretReq{}); return err },
		func() error { _, err := secret.DeleteSecret(ctx, &DeleteSecretReq{}); return err },
		func() error { _, err := secret.ToggleSecretStatus(ctx, &ToggleSecretStatusReq{}); return err },
		func() error { _, err := sys.GetServiceDeployment(ctx, &GetServiceDeploymentReq{}); return err },
		func() error { _, err := sys.CreateServiceDeployment(ctx, &CreateServiceDeploymentReq{}); return err },
		func() error { _, err := sys.UpdateServiceDeployment(ctx, &UpdateServiceDeploymentReq{}); return err },
		func() error { _, err := sys.DeleteServiceDeployment(ctx, &DeleteServiceDeploymentReq{}); return err },
		func() error { _, err := sys.ListActiveServiceDeployments(ctx, &ListActiveServiceDeploymentsReq{}); return err },
	}
	for i, call := range calls {
		assert.Error(t, call(), "call %d should fail without backend", i)
	}
}
