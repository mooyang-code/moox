package deploy

import (
	"fmt"
	"path"
	"slices"
	"sort"
)

// applyOverrides 在渲染之后应用 moox.toml 的 [overrides."<组件>"."<配置文件名>"]：逐项按键路径覆盖，键路径不存在就报错。
// 只处理这次渲染的组件；写了没有这个配置文件的组件同样报错，免得调参悄悄不生效。
func (r *renderer) applyOverrides() error {
	components := make([]string, 0, len(r.Config.Overrides))
	for component := range r.Config.Overrides {
		components = append(components, component)
	}
	sort.Strings(components)
	for _, component := range components {
		if !slices.Contains(r.Components, component) {
			continue
		}
		files := r.Config.Overrides[component]
		names := make([]string, 0, len(files))
		for name := range files {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			where := fmt.Sprintf("[overrides.%q.%q]", component, name)
			target := path.Join("config", component, name)
			file, ok := r.out.Files[target]
			if !ok || path.Ext(name) != ".yaml" {
				return fmt.Errorf("config_invalid: %s：组件 %s 没有 YAML 配置文件 %s", where, component, name)
			}
			document, err := parseYAML(file.Data)
			if err != nil {
				return fmt.Errorf("config_invalid: %s：%w", where, err)
			}
			keys := make([]string, 0, len(files[name]))
			for key := range files[name] {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				if err := setPath(document, key, files[name][key], false); err != nil {
					return fmt.Errorf("config_invalid: %s：%w", where, err)
				}
			}
			if file.Data, err = encodeYAML(document); err != nil {
				return err
			}
			r.out.Files[target] = file
		}
	}
	return nil
}
