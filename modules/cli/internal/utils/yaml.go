package utils

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// ReadYAMLFromFile 从文件中读取YAML数据并解析
func ReadYAMLFromFile(filePath string) (map[string]interface{}, error) {
	// 读取文件内容
	yamlData, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("读取YAML文件失败: %v", err)
	}

	// 解析YAML数据
	var data map[string]interface{}
	err = yaml.Unmarshal(yamlData, &data)
	if err != nil {
		return nil, fmt.Errorf("解析YAML数据失败: %v", err)
	}

	return data, nil
}
