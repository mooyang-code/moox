package engine

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// DetectTotalMemory 探测本机总内存（字节）：Linux 读 /proc/meminfo，macOS 用 sysctl hw.memsize。
func DetectTotalMemory() (int64, error) {
	switch runtime.GOOS {
	case "linux":
		raw, err := os.ReadFile("/proc/meminfo")
		if err != nil {
			return 0, err
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if rest, ok := strings.CutPrefix(line, "MemTotal:"); ok {
				fields := strings.Fields(rest)
				if len(fields) == 0 {
					break
				}
				kb, err := strconv.ParseInt(fields[0], 10, 64)
				if err != nil {
					return 0, err
				}
				return kb << 10, nil
			}
		}
		return 0, errors.New("/proc/meminfo 里没有 MemTotal")
	case "darwin":
		out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
		if err != nil {
			return 0, err
		}
		return strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	}
	return 0, fmt.Errorf("不支持在 %s 上探测内存，请用 --memory 指定", runtime.GOOS)
}

// TuneChange 是对配置文件的一处改动。
type TuneChange struct {
	Path     string
	Old, New string
}

// TuneConfig 把内存建议写进 engine.yaml 的 memory.total_budget、window.max_memory、compute.max_memory，
// 逐键修改、保留注释。已经被用户设置过的值（非 0；compute 非默认的 1GiB）不动，force 为真时覆盖。
func TuneConfig(path string, plan MemoryPlan, force bool) ([]TuneChange, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("解析 %s：%w", path, err)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("引擎配置必须是一个 YAML 映射")
	}
	root := doc.Content[0]
	var changes []TuneChange
	set := func(section, key, value string, isDefault func(old string) bool) {
		table := mappingMember(root, section)
		if table == nil {
			table = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: section}, table)
		}
		node := mappingMember(table, key)
		old := ""
		if node != nil {
			old = node.Value
		}
		if node != nil && !force && !isDefault(old) {
			return
		}
		if node == nil {
			table.Content = append(table.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
		} else {
			node.Kind, node.Tag, node.Value, node.Style = yaml.ScalarNode, "!!str", value, 0
		}
		if old != value {
			changes = append(changes, TuneChange{Path: section + "." + key, Old: old, New: value})
		}
	}
	unset := func(old string) bool { return old == "" || old == "0" }
	set("memory", "total_budget", ConfigValue(plan.TotalBudget), unset)
	set("window", "max_memory", ConfigValue(plan.Window), unset)
	set("compute", "max_memory", ConfigValue(plan.Compute), func(old string) bool { return unset(old) || old == "1GiB" })
	if len(changes) == 0 {
		return nil, nil
	}
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(&doc); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, out.Bytes(), info.Mode().Perm()); err != nil {
		return nil, err
	}
	return changes, nil
}

func mappingMember(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}
