package servicecatalog

import (
	"fmt"
	"path"
	"slices"
	"strings"
)

// Placeholders 是部署字段里能用的占位，部署时换成绝对路径。
var Placeholders = []string{"{root}", "{config}", "{data}", "{logs}", "{secrets}"}

// validateDeployFields 校验部署字段：参数里的占位、配套命令行、配置文件、启动顺序与凭据。
func (c Catalog) validateDeployFields(ids map[string]bool) error {
	binaries := map[string]string{}
	for _, component := range c.Components {
		binaries[component.Binary] = component.ID
	}
	for _, component := range c.Components {
		where := "component " + component.ID
		if len(component.Args) == 0 || len(component.Args) > 64 {
			return fmt.Errorf("%s requires 1..64 args", where)
		}
		for _, arg := range component.Args {
			if arg == "" || len(arg) > 512 || strings.ContainsAny(arg, "\x00\r\n\"'\\%$") {
				return fmt.Errorf("%s has an invalid arg %q", where, arg)
			}
			for _, token := range placeholder.FindAllString(arg, -1) {
				if !slices.Contains(Placeholders, token) {
					return fmt.Errorf("%s arg %q uses unknown placeholder %s", where, arg, token)
				}
			}
		}
		for _, tool := range component.Tools {
			if !binaryName.MatchString(tool) || !strings.HasPrefix(tool, "moox-") || binaries[tool] != "" && binaries[tool] != component.ID {
				return fmt.Errorf("%s has an invalid or shared tool %q", where, tool)
			}
			binaries[tool] = component.ID
		}
		targets := map[string]bool{}
		for _, file := range component.ConfigFiles {
			for _, name := range []string{file.From, file.Target()} {
				if name == "" || path.IsAbs(name) || path.Clean(name) != name || name == "." || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\\\x00\r\n") {
					return fmt.Errorf("%s has an unsafe config file %q", where, name)
				}
			}
			if targets[file.Target()] {
				return fmt.Errorf("%s has a duplicate config file %q", where, file.Target())
			}
			targets[file.Target()] = true
		}
		seen := map[string]bool{}
		for _, dep := range component.After {
			if !ids[dep] || dep == component.ID || seen[dep] {
				return fmt.Errorf("%s has unknown, duplicate or self after %q", where, dep)
			}
			seen[dep] = true
		}
		if component.StopTimeoutSec < 0 || component.StopTimeoutSec > 1800 {
			return fmt.Errorf("%s stop_timeout_sec must be 0..1800", where)
		}
		credentials := map[string]bool{}
		for _, credential := range component.Credentials {
			role, isRole := strings.CutPrefix(credential, "eventbus:")
			if credentials[credential] || credential != "cls" && (!isRole || !identifier.MatchString(role) || role == "metrics-publisher") {
				return fmt.Errorf("%s has an invalid or duplicate credential %q", where, credential)
			}
			credentials[credential] = true
		}
	}
	if _, err := c.StartOrder(nil); err != nil {
		return err
	}
	return nil
}

// StartOrder 按 after 排出启动顺序：ids 为空时排全部组件，否则只排给定的组件（只看它们之间的 after）。
// 没有先后关系的组件按目录顺序。after 成环时报错。
func (c Catalog) StartOrder(ids []string) ([]string, error) {
	selected := []string{}
	for _, component := range c.Components {
		if ids == nil || slices.Contains(ids, component.ID) {
			selected = append(selected, component.ID)
		}
	}
	for _, id := range ids {
		if !slices.Contains(selected, id) {
			return nil, fmt.Errorf("unknown component %q", id)
		}
	}
	order := make([]string, 0, len(selected))
	placed := map[string]bool{}
	for len(order) < len(selected) {
		progressed := false
		for _, id := range selected {
			if placed[id] {
				continue
			}
			component, _ := c.Component(id)
			ready := true
			for _, dep := range component.After {
				if slices.Contains(selected, dep) && !placed[dep] {
					ready = false
					break
				}
			}
			if ready {
				order = append(order, id)
				placed[id] = true
				progressed = true
			}
		}
		if !progressed {
			return nil, fmt.Errorf("component after dependencies contain a cycle")
		}
	}
	return order, nil
}
