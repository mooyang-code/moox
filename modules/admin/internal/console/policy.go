package console

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// methodSpacePolicy 从服务目录读取控制台请求 (serviceID, method) 的三档空间策略：
// global（纯全局）、space_optional（空间可选）、space_required（空间必需，目录省略时的默认值）。
func methodSpacePolicy(catalog servicecatalog.Catalog, serviceID, method string) string {
	return catalog.ConsoleSpacePolicy(serviceID, method)
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

// errInvalidStorageBody 表示请求体不是合法的 JSON 对象，应作为 400 返回。
var errInvalidStorageBody = errors.New("request body must be a JSON object")

// injectStorageSpace 把授权通过的请求头空间写进 Storage 请求体的顶层 space_id，只在 requestBodySpace 要求回填时调用。
// ProtoJSON 同时接受 spaceId 别名（例如值为空串的 spaceId 不算声明了空间），先删掉别名，保证只输出一份。
func injectStorageSpace(body []byte, space string) ([]byte, error) {
	payload := make(map[string]json.RawMessage)
	if len(strings.TrimSpace(string(body))) != 0 {
		if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
			return nil, errInvalidStorageBody
		}
	}
	delete(payload, "spaceId")
	payload["space_id"], _ = json.Marshal(space)
	return json.Marshal(payload)
}

// storageTopLevelSpaceMethods 是请求消息带顶层 space_id 的 Storage 方法，只有它们才能安全地回填请求头空间
// （其余方法的空间在嵌套消息里，如 dataset.space_id，回填会被 ProtoJSON 当作未知字段拒绝）。
// 清单由 TestStorageTopLevelSpaceMethodsMatchProto 对照 .proto 文件校验。
var storageTopLevelSpaceMethods = map[string]bool{
	"DeleteDataSource": true, "DeleteTag": true,
	"AddTagMembers": true, "RemoveTagMembers": true, "SetTagMemberStatus": true, "DeleteDataset": true, "GetSpace": true,
	"GetDataSource": true, "ListDataSources": true, "GetSubject": true, "ListSubjects": true,
	"GetTag": true, "ListTags": true, "ListTagMembers": true, "ResolveSubjects": true,
	"GetDataset": true, "ListDatasets": true, "ListDatasetSubjects": true,
	"ListDatasetColumns": true, "QueryTimeSeriesRows": true, "SearchRecordRows": true, "GetViewState": true,
}
