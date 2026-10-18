package console

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// globalAdminRole 是全局管理员的角色值。
const globalAdminRole = 2

// 第一组服务必须携带 X-Space-Id；第二组（Storage、Factor）既有空间内数据，
// 也有全局运维接口，请求里没有空间时按全局运维处理。
func requiresSpaceHeader(serviceID string) bool {
	switch serviceID {
	case "trade", "strategy", "collector", "cloudnode", "archive":
		return true
	}
	return false
}

func spaceOptional(serviceID string) bool {
	return serviceID == "storage" || serviceID == "factor-mgr"
}

func isSpaceScopedService(serviceID string) bool {
	return requiresSpaceHeader(serviceID) || spaceOptional(serviceID)
}

// adminOnlyWriteService 的写方法没有空间归属，只允许全局管理员，普通登录用户不能改全局状态。
func adminOnlyWriteService(serviceID string) bool {
	switch serviceID {
	case "ssh", "secret", "sysdeploy", "setup", "monitor", "space":
		return true
	}
	return false
}

// adminOnlyService 的全部方法（含读）只允许全局管理员：主机的 SSH 配置、会话和文件传输等价于登录这些主机，
// 普通用户不能看到、也不能间接使用。
// adminOnlyMethods 是空间管理员也不能调用、只允许全局管理员的方法。
//
// 因子定义没有空间归属（按全局 factor_id 定位，可以被多个空间的因子集引用），空间管理员改定义会影响其他空间：
// 创建与修改只允许全局管理员。校验表达式（ValidateFactorSpec）与函数清单是无副作用的读；试算（PreviewFactor）
// 按因子集的空间授权，只读该空间的源数据。
//
// 云账户没有空间归属（表里没有 space 字段，按 account_id 全局定位），创建是 UPSERT，空间管理员改它会影响其他空间共用的账户，
// 所以写方法同样只允许全局管理员。
//
// 推送渠道的 Webhook URL 本身就是推送凭据（拿到即可往群里发消息），列出渠道的接口只给全局管理员；
// 其他登录用户从监控概览只能看到渠道数量和平台。增删渠道是 monitor 的写方法，本来就只允许全局管理员。
//
// 秘钥导出的迁移包里有全部秘钥的明文（用导出口令加密），导入会覆盖秘钥：都只允许全局管理员。secret 的写方法本来就要求
// 全局管理员，这里显式列出，导出即使以后被标为只读（可重试）也不会放宽。
var adminOnlyMethods = map[string]bool{
	"monitor/ListNotificationChannels": true,
	"secret/ExportSecrets":             true, "secret/ImportSecrets": true,
	"factor-mgr/CreateFactor": true, "factor-mgr/UpdateFactor": true,
	// 策略定义没有空间归属（按全局 strategy_id 定位），实例与回放按空间授权：空间管理员改定义会影响其他空间已引用它的实例。
	"strategy/CreateStrategy": true, "strategy/UpdateStrategy": true, "strategy/DeleteStrategy": true,
	"cloudnode/CreateCloudAccount": true, "cloudnode/UpdateCloudAccount": true, "cloudnode/DeleteCloudAccount": true,
}

func adminOnlyMethod(serviceID, method string) bool { return adminOnlyMethods[serviceID+"/"+method] }

func adminOnlyService(serviceID string) bool {
	return serviceID == "ssh"
}

// bodySpaceIDs 收集请求体（含嵌套对象）里出现的 space_id。
func bodySpaceIDs(body []byte) ([]string, error) {
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil, nil
	}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return nil, fmt.Errorf("invalid space-scoped request body: %w", err)
	}
	var ids []string
	var walk func(any)
	walk = func(item any) {
		switch node := item.(type) {
		case map[string]any:
			for key, child := range node {
				if strings.EqualFold(key, "space_id") || strings.EqualFold(key, "spaceId") {
					if text, ok := child.(string); ok && strings.TrimSpace(text) != "" {
						ids = append(ids, strings.TrimSpace(text))
					}
				}
				walk(child)
			}
		case []any:
			for _, child := range node {
				walk(child)
			}
		}
	}
	walk(value)
	return ids, nil
}

// effectiveSpaceID 返回请求授权所针对的空间：请求头优先；没有请求头时取请求体里
// 唯一的 space_id。请求体里出现与授权空间不一致的 space_id 一律拒绝。
func effectiveSpaceID(header string, body []byte) (string, error) {
	header = strings.TrimSpace(header)
	ids, err := bodySpaceIDs(body)
	if err != nil {
		return "", err
	}
	space := header
	for _, id := range ids {
		if space == "" {
			space = id
		}
		if id != space {
			return "", fmt.Errorf("space_id %q does not match authorized space %q", id, space)
		}
	}
	return space, nil
}

// storageGlobalMethods 没有空间归属（节点、归档、空间目录本身），读写都只允许全局管理员，
// 不能靠请求头声明某个空间来获得授权。
var storageGlobalMethods = map[string]bool{
	"CreateSpace": true, "UpdateSpace": true, "DeleteSpace": true, "ListSpaces": true,
	"UpdateDataNode": true, "DeleteDataNode": true, "GetDataNode": true, "ListDataNodes": true,
	"RebindDatasetDataNode": true,
}

// requestBodySpace 是空间内 Storage 方法实际作用的空间。请求体声明的空间必须唯一且与请求头一致；
// 请求体完全没有声明且该方法的请求消息有顶层 space_id 时，采用请求头的空间并要求调用方写入请求体（inject），否则后端会按空
// space_id 查询所有空间。请求头和请求体都没有空间则拒绝。
func requestBodySpace(header string, body []byte, canInject bool) (space string, inject bool, err error) {
	ids, err := bodySpaceIDs(body)
	if err != nil {
		return "", false, err
	}
	header = strings.TrimSpace(header)
	if len(ids) == 0 {
		if header == "" || !canInject {
			return "", false, errors.New("space_id is required in the request body")
		}
		return header, true, nil
	}
	for _, id := range ids {
		if id != ids[0] || header != "" && id != header {
			return "", false, fmt.Errorf("space_id %q does not match authorized space %q", id, header)
		}
	}
	return ids[0], false, nil
}

// storageTopLevelSpaceMethods 是请求消息带顶层 space_id 的 Storage 方法，只有它们才能安全地回填请求头空间
// （其余方法的空间在嵌套消息里，如 view.space_id，回填会被 ProtoJSON 当作未知字段拒绝）。
// 清单由 TestStorageTopLevelSpaceMethodsMatchProto 对照 .proto 文件校验。
var storageTopLevelSpaceMethods = map[string]bool{
	"DeleteView": true, "RequestViewRebuild": true, "DeleteDataSource": true, "DeleteTag": true,
	"AddTagMembers": true, "RemoveTagMembers": true, "SetTagMemberStatus": true, "DeleteDataset": true,
	"ActivateDataset": true, "BatchUpdateFields": true, "DeleteFieldGroup": true, "GetSpace": true,
	"GetView": true, "ListViews": true, "ListViewColumns": true, "ListViewRebuildLogs": true,
	"GetDataSource": true, "ListDataSources": true, "GetSubject": true, "ListSubjects": true,
	"GetTag": true, "ListTags": true, "ListTagMembers": true, "ResolveSubjects": true,
	"GetDataset": true, "ListDatasets": true, "CheckDatasetActivation": true, "ListDatasetSubjects": true,
	"GetFieldGroup": true, "ListFieldGroups": true, "GetField": true, "ListFields": true,
	"ListDatasetColumns": true, "QueryTimeSeriesRows": true, "SearchRecordRows": true,
}
