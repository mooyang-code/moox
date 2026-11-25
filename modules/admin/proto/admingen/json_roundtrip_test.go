package adminpb

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/codec"
)

// Admin 接口里的时刻都是 Unix 毫秒；经 trpc 的 JSON 序列化往返不变，JSON 里是数字字符串。
func TestAdminTRPCJSONRoundTrip(t *testing.T) {
	const at = int64(1700000000123)
	assertTRPCJSONRoundTrip(t, &ListActivityLogsRsp{Items: []*ActivityLogItem{{Id: 1, Title: "t", CreatedAt: at}}}, new(ListActivityLogsRsp),
		map[string]int64{"items.0.created_at": at})
	assertTRPCJSONRoundTrip(t, &ListActivityLogsReq{SpaceId: "crypto", Since: at, Until: at + 1}, new(ListActivityLogsReq),
		map[string]int64{"since": at, "until": at + 1})
	assertTRPCJSONRoundTrip(t, &LoginRsp{UserInfo: &UserInfo{UserId: "u", CreatedAt: at, LastLoginAt: at + 1}, ExpiresAt: at + 2}, new(LoginRsp),
		map[string]int64{"user_info.created_at": at, "user_info.last_login_at": at + 1, "expires_at": at + 2})
	assertTRPCJSONRoundTrip(t, &SftpListRsp{Files: []*SftpFileItem{{Name: "a", ModifiedAt: at}}}, new(SftpListRsp),
		map[string]int64{"files.0.modified_at": at})
	assertTRPCJSONRoundTrip(t, &GetSecretRsp{Secret: &Secret{SecretId: "s", CreatedAt: at, UpdatedAt: at + 1}}, new(GetSecretRsp),
		map[string]int64{"secret.created_at": at, "secret.updated_at": at + 1})
	assertTRPCJSONRoundTrip(t, &ListDeploymentHostsRsp{Hosts: []*DeploymentHost{{HostId: "control", CreatedAt: at, UpdatedAt: at + 1}}}, new(ListDeploymentHostsRsp),
		map[string]int64{"hosts.0.created_at": at, "hosts.0.updated_at": at + 1})
	assertTRPCJSONRoundTrip(t, &ListDeploymentsRsp{Deployments: []*ComponentDeployment{{HostId: "control", ComponentId: "admin", CreatedAt: at, UpdatedAt: at + 1}}}, new(ListDeploymentsRsp),
		map[string]int64{"deployments.0.created_at": at, "deployments.0.updated_at": at + 1})
	assertTRPCJSONRoundTrip(t, &GetHostRoutesRsp{HostId: "control", CompiledAt: at, GatewayStatus: &HostGatewayRuntimeStatus{LastSeenAt: at + 1,
		RelaySessions: []*RelaySessionStatus{{HostId: "home", Online: true, ConnectedAt: at + 2}}}}, new(GetHostRoutesRsp),
		map[string]int64{"compiled_at": at, "gateway_status.last_seen_at": at + 1, "gateway_status.relay_sessions.0.connected_at": at + 2})
}

// assertTRPCJSONRoundTrip 用服务端实际使用的 JSON 序列化（trpc-go 注册的 SerializationTypeJSON，即 protojson）编码再解码：
// 解码后与原消息相同，want 里每个字段路径（点分，数组用下标）在 JSON 里都是十进制数字字符串。
func assertTRPCJSONRoundTrip(t *testing.T, message, decoded proto.Message, want map[string]int64) {
	t.Helper()
	serializer := codec.GetSerializer(codec.SerializationTypeJSON)
	raw, err := serializer.Marshal(message)
	if err != nil {
		t.Fatalf("marshal %T: %v", message, err)
	}
	if err := serializer.Unmarshal(raw, decoded); err != nil {
		t.Fatalf("unmarshal %T: %v", message, err)
	}
	if !proto.Equal(message, decoded) {
		t.Fatalf("round trip of %T changed the message:\n got %v\nwant %v", message, decoded, message)
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	for path, value := range want {
		node := tree
		for _, part := range strings.Split(path, ".") {
			if index, err := strconv.Atoi(part); err == nil {
				items, _ := node.([]any)
				if index >= len(items) {
					t.Fatalf("%s: index %d out of range in %s", path, index, raw)
				}
				node = items[index]
				continue
			}
			object, _ := node.(map[string]any)
			node = object[part]
		}
		if got, ok := node.(string); !ok || got != strconv.FormatInt(value, 10) {
			t.Fatalf("%s = %#v, want decimal string %q in %s", path, node, strconv.FormatInt(value, 10), raw)
		}
	}
}
