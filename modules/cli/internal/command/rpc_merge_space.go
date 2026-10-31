package command

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// mergeSpace 取回现有空间再合并：SpaceMgr 的 UpdateSpace 整体替换（没给的字段会被清空，状态为空时还会变回 active），
// 而它没有 Get 接口，所以用 ListSpaces 按 space_id 找到现有的那一条。
var mergeSpace rpcMerge = mergeExisting("space", []string{"space_id", "name", "description", "owner", "market", "timezone", "status", "attributes"},
	func(ctx context.Context, inv rpcInvocation, request map[string]any) (map[string]any, error) {
		spaceID, ok := lookupRequestString(request, "space.space_id")
		if !ok {
			return nil, fmt.Errorf("缺少 space.space_id")
		}
		for page := 1; page <= 20; page++ {
			list := inv
			list.Method = "ListSpaces"
			list.Request = map[string]any{"page": map[string]any{"page": page, "size": 200}}
			response, err := invokeRPC(ctx, list)
			if err != nil {
				return nil, err
			}
			if err := rpcRetError(response); err != nil {
				return nil, err
			}
			var decoded struct {
				Spaces []map[string]any `json:"spaces"`
				Page   struct {
					HasMore bool `json:"has_more"`
				} `json:"page_result"`
			}
			if err := json.Unmarshal(response, &decoded); err != nil {
				return nil, err
			}
			for _, item := range decoded.Spaces {
				if id, _ := item["space_id"].(string); strings.EqualFold(id, spaceID) {
					return item, nil
				}
			}
			if !decoded.Page.HasMore {
				break
			}
		}
		return nil, fmt.Errorf("空间 %s 不存在", spaceID)
	})
