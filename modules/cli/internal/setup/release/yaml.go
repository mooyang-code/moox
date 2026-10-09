package release

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// yamlDoc 是一份可以按路径改写的 YAML 文档，保留注释和键的顺序。
type yamlDoc struct {
	name string
	root *yaml.Node
}

func parseYAML(name string, raw []byte) (*yamlDoc, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("解析 %s: %w", name, err)
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s 的顶层必须是映射", name)
	}
	return &yamlDoc{name: name, root: root.Content[0]}, nil
}

// lookup 返回路径对应的节点；路径用点分隔，例如 gateway_client.key_file。
func (d *yamlDoc) lookup(path string) (*yaml.Node, bool) {
	node := d.root
	for _, key := range strings.Split(path, ".") {
		if node.Kind != yaml.MappingNode {
			return nil, false
		}
		found := false
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == key {
				node = node.Content[i+1]
				found = true
				break
			}
		}
		if !found {
			return nil, false
		}
	}
	return node, true
}

// set 改写已有的键；键不存在时报错，避免模块配置改名后渲染悄悄失效。
func (d *yamlDoc) set(path string, value any) error {
	node, ok := d.lookup(path)
	if !ok {
		return fmt.Errorf("%s 中没有 %s", d.name, path)
	}
	return assign(node, value)
}

// remove 删除一个键；键不存在时什么也不做。
func (d *yamlDoc) remove(path string) {
	parentPath, key := "", path
	if i := strings.LastIndex(path, "."); i >= 0 {
		parentPath, key = path[:i], path[i+1:]
	}
	parent := d.root
	if parentPath != "" {
		var ok bool
		if parent, ok = d.lookup(parentPath); !ok || parent.Kind != yaml.MappingNode {
			return
		}
	}
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			parent.Content = append(parent.Content[:i], parent.Content[i+2:]...)
			return
		}
	}
}

// setServiceIP 改写 tRPC 配置中 server.service 下某个服务的监听地址。
func (d *yamlDoc) setServiceIP(service, ip string) error {
	services, ok := d.lookup("server.service")
	if !ok || services.Kind != yaml.SequenceNode {
		return fmt.Errorf("%s 中没有 server.service", d.name)
	}
	for _, item := range services.Content {
		if item.Kind != yaml.MappingNode {
			continue
		}
		name, ip0 := -1, -1
		for i := 0; i+1 < len(item.Content); i += 2 {
			switch item.Content[i].Value {
			case "name":
				if item.Content[i+1].Value == service {
					name = i
				}
			case "ip":
				ip0 = i
			}
		}
		if name < 0 {
			continue
		}
		if ip0 < 0 {
			return fmt.Errorf("%s 中的服务 %s 没有 ip", d.name, service)
		}
		item.Content[ip0+1].Value = ip
		return nil
	}
	return fmt.Errorf("%s 中没有服务 %s", d.name, service)
}

// setLogPath 把 tRPC 文件日志写到指定目录；只有控制台输出时什么也不做。
func (d *yamlDoc) setLogPath(dir string) {
	writers, ok := d.lookup("plugins.log.default")
	if !ok || writers.Kind != yaml.SequenceNode {
		return
	}
	for _, writer := range writers.Content {
		if writer.Kind != yaml.MappingNode {
			continue
		}
		for i := 0; i+1 < len(writer.Content); i += 2 {
			if writer.Content[i].Value != "writer_config" || writer.Content[i+1].Kind != yaml.MappingNode {
				continue
			}
			config := writer.Content[i+1]
			for j := 0; j+1 < len(config.Content); j += 2 {
				if config.Content[j].Value == "log_path" {
					config.Content[j+1].Value = dir
					config.Content[j+1].Style = 0
				}
			}
		}
	}
}

func (d *yamlDoc) bytes() ([]byte, error) {
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{d.root}}); err != nil {
		return nil, fmt.Errorf("生成 %s: %w", d.name, err)
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// assign 把 Go 值写入节点，保留节点上的注释。
func assign(node *yaml.Node, value any) error {
	head, line, foot := node.HeadComment, node.LineComment, node.FootComment
	var fresh yaml.Node
	switch v := value.(type) {
	case string:
		fresh = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
	case bool:
		fresh = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(v)}
	case int:
		fresh = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(v)}
	case []string:
		fresh = yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
		for _, item := range v {
			fresh.Content = append(fresh.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: item})
		}
	default:
		if err := fresh.Encode(v); err != nil {
			return fmt.Errorf("编码 YAML 值: %w", err)
		}
	}
	*node = fresh
	node.HeadComment, node.LineComment, node.FootComment = head, line, foot
	return nil
}
