package accessproxy

import (
	"fmt"
	"sort"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// LoadPrincipalKeys 读取外部调用方的密钥集（Admin 的 keys export-principals 导出，0600 的 JSON），返回校验用的
// 密钥注册表和其中出现的外部调用方。每把密钥的调用方都必须是组件目录中定义的外部调用方。
func LoadPrincipalKeys(path string, catalog *servicecatalog.Catalog) (*gatewayauth.CredentialRegistry, []string, error) {
	if catalog == nil {
		catalog = servicecatalog.Default()
	}
	credentials, err := gatewayauth.LoadKeySet(path)
	if err != nil {
		return nil, nil, err
	}
	seen := map[string]bool{}
	for _, credential := range credentials {
		if _, ok := catalog.Principal(credential.Caller); !ok {
			return nil, nil, fmt.Errorf("密钥集 %s 中的 %s 不是组件目录中的外部调用方", path, credential.Caller)
		}
		seen[credential.Caller] = true
	}
	registry, err := gatewayauth.NewCredentialRegistry(credentials)
	if err != nil {
		return nil, nil, fmt.Errorf("密钥集 %s: %w", path, err)
	}
	principals := make([]string, 0, len(seen))
	for principal := range seen {
		principals = append(principals, principal)
	}
	sort.Strings(principals)
	return registry, principals, nil
}
