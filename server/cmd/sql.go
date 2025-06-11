package main

import (
	"bufio"
	"database/sql"
	"fmt"
	"io/ioutil"
	"strings"
)

// readSQLFromFile 从文件中读取SQL语句，参考 cli/utils/sql.go 的实现
func readSQLFromFile(filePath string) ([]string, error) {
	// 读取文件
	content, err := ioutil.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	// 将内容分割成多个SQL语句
	sqlText := string(content)
	// 处理不同的换行符
	sqlText = strings.ReplaceAll(sqlText, "\r\n", "\n")

	// 分割SQL语句（按分号分割）
	var statements []string
	scanner := bufio.NewScanner(strings.NewReader(sqlText))
	scanner.Split(bufio.ScanLines)

	var currentStatement strings.Builder
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// 跳过整行注释和空行
		if strings.HasPrefix(line, "--") || strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		// 处理行尾注释
		if idx := strings.Index(line, "--"); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
		}
		if idx := strings.Index(line, "#"); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
		}
		if line == "" {
			continue
		}

		currentStatement.WriteString(line + " ")
		if strings.HasSuffix(line, ";") {
			stmt := strings.TrimSpace(currentStatement.String())
			if stmt != "" && stmt != ";" {
				statements = append(statements, stmt)
			}
			currentStatement.Reset()
		}
	}

	// 处理最后一个可能没有分号结尾的语句
	lastStmt := strings.TrimSpace(currentStatement.String())
	if lastStmt != "" && lastStmt != ";" {
		statements = append(statements, lastStmt)
	}

	return statements, nil
}

// executeSQLStatements 执行SQL语句列表
func executeSQLStatements(db *sql.DB, statements []string) error {
	for i, stmt := range statements {
		stmt = strings.TrimSpace(stmt)

		// 跳过空语句
		if stmt == "" {
			continue
		}

		fmt.Printf("%s正在执行第 %d 条 SQL 语句...%s\n", ColorBlue, i+1, ColorReset)

		// 显示语句预览（前80个字符）
		preview := stmt
		if len(preview) > 80 {
			preview = preview[:80] + "..."
		}
		fmt.Printf("%s语句: %s%s\n", ColorCyan, preview, ColorReset)

		// 执行 SQL 语句
		if _, err := db.Exec(stmt); err != nil {
			// 如果是"already exists"错误，只是警告而不是失败
			if strings.Contains(err.Error(), "already exists") {
				fmt.Printf("%s⚠ 警告: %v%s\n", ColorYellow, err, ColorReset)
				continue
			}
			return fmt.Errorf("执行 SQL 语句失败 (第 %d 条): %v\n语句: %s", i+1, err, stmt)
		}

		fmt.Printf("%s✓ 第 %d 条 SQL 语句执行成功%s\n", ColorGreen, i+1, ColorReset)
	}

	return nil
}
