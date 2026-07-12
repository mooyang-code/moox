package mooxpb

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func allProtoMessages() []proto.Message {
	return []proto.Message{
		&AppInfo{}, &ChangePasswordReq{}, &ChangePasswordRsp{},
		&CreateHostReq{Host: &SSHHost{}}, &CreateHostRsp{},
		&CreateSecretReq{Secret: &Secret{}}, &CreateSecretRsp{},
		&CreateServiceDeploymentReq{Deployment: &ServiceDeployment{}}, &CreateServiceDeploymentRsp{},
		&CreateSessionReq{}, &CreateSessionRsp{},
		&CreateSpaceReq{Space: &Space{}}, &CreateSpaceRsp{Space: &Space{}},
		&DNSRecord{IpList: []*IPInfo{{}}}, &DeleteHostReq{}, &DeleteHostRsp{},
		&DeleteSecretReq{}, &DeleteSecretRsp{},
		&DeleteServiceDeploymentReq{}, &DeleteServiceDeploymentRsp{},
		&DisconnectSessionReq{}, &DisconnectSessionRsp{},
		&ForceDisconnectReq{}, &ForceDisconnectRsp{},
		&GetChangePasswordSaltReq{}, &GetChangePasswordSaltRsp{},
		&GetDNSRecordReq{}, &GetDNSRecordRsp{Record: &DNSRecord{}},
		&GetHostReq{}, &GetHostRsp{Host: &SSHHost{}},
		&GetLoginSaltReq{}, &GetLoginSaltRsp{},
		&GetOnlineSessionsReq{}, &GetOnlineSessionsRsp{Sessions: []*SessionInfo{{}}},
		&GetSecretReq{}, &GetSecretRsp{Secret: &Secret{}},
		&GetServiceDeploymentReq{}, &GetServiceDeploymentRsp{Deployment: &ServiceDeployment{}},
		&GetUserInfoReq{}, &GetUserInfoRsp{UserInfo: &UserInfo{}},
		&IPInfo{}, &ListActiveServiceDeploymentsReq{},
		&ListActiveServiceDeploymentsRsp{Deployments: []*ServiceDeployment{{}}},
		&ListDNSRecordsReq{}, &ListDNSRecordsRsp{Records: []*DNSRecord{{}}},
		&ListHostsReq{}, &ListHostsRsp{Hosts: []*SSHHost{{}}},
		&ListSecretsReq{}, &ListSecretsRsp{Secrets: []*Secret{{}}},
		&ListServiceDeploymentsReq{}, &ListServiceDeploymentsRsp{Deployments: []*ServiceDeployment{{}}},
		&ListSpaceMembersReq{}, &ListSpaceMembersRsp{Members: []*SpaceMember{{}}},
		&ListSpacesReq{}, &ListSpacesRsp{Spaces: []*Space{{}}},
		&LoginReq{}, &LoginRsp{RetInfo: &RetInfo{}, UserInfo: &UserInfo{}},
		&PathBreadcrumb{}, &RegisterReq{}, &RegisterRsp{UserInfo: &UserInfo{}},
		&ResizeWindowReq{}, &ResizeWindowRsp{},
		&RevealSecretReq{}, &RevealSecretRsp{Secret: &Secret{}},
		&SSHHost{}, &Secret{}, &ServiceDeployment{},
		&ServiceDeploymentEndpoint{}, &ServiceDeploymentWarning{},
		&SessionInfo{}, &SftpDeleteReq{}, &SftpDeleteRsp{},
		&SftpFileItem{}, &SftpListReq{}, &SftpListRsp{Files: []*SftpFileItem{{}}},
		&SftpMkdirReq{}, &SftpMkdirRsp{}, &Space{}, &SpaceMember{},
		&ToggleSecretStatusReq{}, &ToggleSecretStatusRsp{},
		&UpdateHostReq{Host: &SSHHost{}}, &UpdateHostRsp{},
		&UpdateSecretReq{}, &UpdateSecretRsp{},
		&UpdateServiceDeploymentReq{Deployment: &ServiceDeployment{}}, &UpdateServiceDeploymentRsp{},
		&UpdateSpaceReq{Space: &Space{}}, &UpdateSpaceRsp{},
		&UpdateUserInfoReq{}, &UpdateUserInfoRsp{UserInfo: &UserInfo{}},
		&UserInfo{},
	}
}

func TestAllProtoMessages_ReflectGettersAndFields(t *testing.T) {
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
				if pr.Has(fd) {
					_ = pr.Get(fd)
				}
			}
			invokeGetters(t, msg)
		})
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
