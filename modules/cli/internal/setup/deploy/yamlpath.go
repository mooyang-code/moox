package deploy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// parseYAML 读一份只有一个映射的 YAML 文档，保留注释与键的顺序。
func parseYAML(raw []byte) (*yaml.Node, error) {
	var document yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("不是合法的 YAML：%w", err)
	}
	if decoder.Decode(new(any)) != io.EOF || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("要求恰好一个 YAML 映射文档")
	}
	return &document, nil
}

func encodeYAML(document *yaml.Node) ([]byte, error) {
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

// member 取映射里的一个键，没有时返回 nil。
func member(node *yaml.Node, key string) *yaml.Node {
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

// setPath 按点号分隔的键路径写一个值。中间各级必须已经是映射；最后一级在 create 为真时可以新建，否则必须已存在。
func setPath(document *yaml.Node, keyPath string, value any, create bool) error {
	node := document
	if node.Kind == yaml.DocumentNode {
		node = node.Content[0]
	}
	keys := strings.Split(keyPath, ".")
	for _, key := range keys[:len(keys)-1] {
		next := member(node, key)
		if next == nil || next.Kind != yaml.MappingNode {
			return fmt.Errorf("键路径 %s 不存在（%s 不是映射）", keyPath, key)
		}
		node = next
	}
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("键路径 %s 不存在", keyPath)
	}
	var encoded yaml.Node
	if err := encoded.Encode(value); err != nil {
		return fmt.Errorf("键路径 %s 的值无法编码", keyPath)
	}
	last := keys[len(keys)-1]
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == last {
			// 保留这一行原有的行尾注释。
			encoded.LineComment = node.Content[i+1].LineComment
			node.Content[i+1] = &encoded
			return nil
		}
	}
	if !create {
		return fmt.Errorf("键路径 %s 不存在", keyPath)
	}
	node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: last}, &encoded)
	return nil
}

// hasPath 报告键路径是否存在。
func hasPath(document *yaml.Node, keyPath string) bool {
	node := document
	if node.Kind == yaml.DocumentNode {
		node = node.Content[0]
	}
	for _, key := range strings.Split(keyPath, ".") {
		if node = member(node, key); node == nil {
			return false
		}
	}
	return true
}
