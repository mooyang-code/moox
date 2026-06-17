package utils

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// ReadSQLFromFile 从文件中读取SQL语句
func ReadSQLFromFile(filePath string) ([]string, error) {
	// 读取文件
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	// 将内容分割成多个SQL语句
	sqlText := string(content)
	// 处理不同的换行符
	sqlText = strings.ReplaceAll(sqlText, "\r\n", "\n")

	// 分割SQL语句（按分号分割，但要考虑复合语句如触发器）
	var statements []string
	scanner := bufio.NewScanner(strings.NewReader(sqlText))
	scanner.Split(bufio.ScanLines)

	var currentStatement strings.Builder
	inTrigger := false
	beginCount := 0
	
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// 跳过整行注释和空行
		if strings.HasPrefix(line, "--") || strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		// 处理行尾注释（需要跳过引号内的内容）
		line = stripInlineComment(line)
		if line == "" {
			continue
		}

		// 检测是否进入触发器或存储过程
		lineUpper := strings.ToUpper(line)
		if strings.Contains(lineUpper, "CREATE TRIGGER") || strings.Contains(lineUpper, "CREATE PROCEDURE") {
			inTrigger = true
		}
		
		// 计算BEGIN和END的嵌套层数
		if strings.Contains(lineUpper, "BEGIN") {
			beginCount++
		}
		if strings.Contains(lineUpper, "END;") {
			beginCount--
		}

		currentStatement.WriteString(line + " ")
		
		// 判断语句是否结束
		if strings.HasSuffix(line, ";") {
			// 如果在触发器/存储过程中，只有在BEGIN/END平衡且遇到END;时才结束
			if inTrigger && beginCount > 0 {
				continue // 继续读取，还在复合语句内
			}
			
			// 语句结束
			stmt := strings.TrimSpace(currentStatement.String())
			if stmt != "" && stmt != ";" {
				statements = append(statements, stmt)
			}
			currentStatement.Reset()
			inTrigger = false
			beginCount = 0
		}
	}

	// 处理最后一个可能没有分号结尾的语句
	lastStmt := strings.TrimSpace(currentStatement.String())
	if lastStmt != "" && lastStmt != ";" {
		statements = append(statements, lastStmt)
	}

	return statements, nil
}

// ExtractTableName 提取建表语句中的表名
func ExtractTableName(sqlStmt string) string {
	// 忽略大小写和多余空格
	sqlStmt = strings.TrimSpace(sqlStmt)

	// 如果不是CREATE TABLE语句，返回空
	if !strings.HasPrefix(strings.ToUpper(sqlStmt), "CREATE TABLE") {
		return ""
	}

	// 使用正则表达式匹配表名，支持CREATE TABLE和CREATE TABLE IF NOT EXISTS两种语法
	// 同时支持表名被引号(`, ", ')包围的情况
	re := regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?[\s]*([` + "`" + `"']?)([^\s` + "`" + `"'()]+)([` + "`" + `"']?)`)
	matches := re.FindStringSubmatch(sqlStmt)

	if len(matches) >= 4 {
		// matches[1]是左引号，matches[2]是表名，matches[3]是右引号
		tableName := matches[2]
		// 如果表名包含schema前缀（如myschema.mytable），只返回表名部分
		parts := strings.Split(tableName, ".")
		return parts[len(parts)-1]
	}
	return ""
}

// ExtractTableSchema 提取建表语句中的表结构定义
func ExtractTableSchema(sqlStmt string) string {
	// 忽略大小写和多余空格
	sqlStmt = strings.TrimSpace(sqlStmt)

	// 如果不是CREATE TABLE语句，返回空
	if !strings.HasPrefix(strings.ToUpper(sqlStmt), "CREATE TABLE") {
		return ""
	}

	// 找到第一个左括号和最后一个右括号
	startPos := strings.Index(sqlStmt, "(")
	endPos := strings.LastIndex(sqlStmt, ")")

	if startPos == -1 || endPos == -1 || startPos >= endPos {
		// 尝试正则表达式提取
		re := regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?[\w\.\` + "`" + `"']+\s*\((.*)\)`)
		matches := re.FindStringSubmatch(sqlStmt)

		if len(matches) >= 2 {
			return processTableSchema(strings.TrimSpace(matches[1]))
		}

		fmt.Printf("警告: 无法从SQL语句中提取表结构: %s\n", sqlStmt)
		return ""
	}

	// 提取括号内的内容
	schemaContent := strings.TrimSpace(sqlStmt[startPos+1 : endPos])
	return processTableSchema(schemaContent)
}

// processTableSchema 处理表结构定义，删除注释
func processTableSchema(schema string) string {
	lines := strings.Split(schema, ",")
	for i, line := range lines {
		// 删除行尾注释
		if idx := strings.Index(line, "--"); idx >= 0 {
			lines[i] = strings.TrimSpace(line[:idx])
		}
	}
	return strings.Join(lines, ",")
}

// stripInlineComment 去除行尾的 SQL 注释（-- 或 #），但跳过单引号内的内容
func stripInlineComment(line string) string {
	inQuote := false
	for i := 0; i < len(line); i++ {
		ch := line[i]
		if ch == '\'' {
			inQuote = !inQuote
			continue
		}
		if inQuote {
			continue
		}
		// 检测 -- 注释
		if ch == '-' && i+1 < len(line) && line[i+1] == '-' {
			return strings.TrimSpace(line[:i])
		}
		// 检测 # 注释
		if ch == '#' {
			return strings.TrimSpace(line[:i])
		}
	}
	return line
}
