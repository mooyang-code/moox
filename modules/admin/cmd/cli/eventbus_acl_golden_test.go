package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	yamlv3 "gopkg.in/yaml.v3"
)

var updateACLGolden = flag.Bool("update-acl-golden", false, "重写 testdata/eventbus-acl.json")

type aclGoldenUser struct {
	Publish   []string `json:"publish"`
	Subscribe []string `json:"subscribe"`
	Responses string   `json:"responses,omitempty"`
}

// TestEventBusACLGolden 把生成的 users.yaml 解析成“用户 → 允许集合”与金样比对：拓扑声明的任何改动都要在金样里看得见。
func TestEventBusACLGolden(t *testing.T) {
	tokens := map[string]string{}
	for _, role := range eventBusRoles {
		tokens[role] = "token-" + role
	}
	var parsed struct {
		Users []struct {
			Username    string `yaml:"username"`
			Permissions struct {
				Publish struct {
					Allow []string `yaml:"allow"`
				} `yaml:"publish"`
				Subscribe struct {
					Allow []string `yaml:"allow"`
				} `yaml:"subscribe"`
				Responses map[string]any `yaml:"responses"`
			} `yaml:"permissions"`
		} `yaml:"users"`
	}
	require.NoError(t, yamlv3.Unmarshal([]byte(usersYAML(tokens)), &parsed))
	got := map[string]aclGoldenUser{}
	for _, user := range parsed.Users {
		_, duplicated := got[user.Username]
		require.False(t, duplicated, user.Username)
		item := aclGoldenUser{Publish: append([]string(nil), user.Permissions.Publish.Allow...), Subscribe: append([]string(nil), user.Permissions.Subscribe.Allow...)}
		slices.Sort(item.Publish)
		slices.Sort(item.Subscribe)
		if len(user.Permissions.Responses) > 0 {
			raw, err := json.Marshal(user.Permissions.Responses)
			require.NoError(t, err)
			item.Responses = string(raw)
		}
		got[user.Username] = item
	}
	path := filepath.Join("testdata", "eventbus-acl.json")
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	require.NoError(t, encoder.Encode(got))
	raw := buffer.Bytes()
	if *updateACLGolden {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(path, raw, 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err)
	require.JSONEq(t, string(want), string(raw))
}

// 服务目录里每个组件要的 eventbus:<角色> 都必须是拓扑里声明过的账号（按凭据文件名对上）。
func TestCatalogEventBusRolesAreDeclared(t *testing.T) {
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	for _, component := range catalog.Components {
		for _, file := range component.EventBusRoles() {
			_, ok := events.RoleByFile(file)
			require.True(t, ok, "组件 %s 要的 EventBus 角色 %s 没有在 packages/events 拓扑里声明", component.ID, file)
		}
	}
}
