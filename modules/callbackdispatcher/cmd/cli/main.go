// moox-callback-dispatcher-cli 只有一个命令：init，在不启动服务的情况下创建事件中心的数据库结构。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mooyang-code/moox/modules/callbackdispatcher/internal/store"
	"github.com/mooyang-code/moox/modules/callbackdispatcher/schema"
)

const defaultDBPath = "./data/moox_callback_dispatcher.db"

type initResult struct {
	Module string `json:"module"`
	Action string `json:"action"`
	Status string `json:"status"`
	DBPath string `json:"db_path"`
}

func main() {
	if len(os.Args) < 2 || os.Args[1] != "init" {
		printError(os.Stderr, fmt.Errorf("未知命令：请使用 init"))
		os.Exit(2)
	}
	if err := runInit(os.Args[2:], os.Stdout, os.Stderr); err != nil {
		printError(os.Stderr, err)
		os.Exit(1)
	}
}

func runInit(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dbPath := defaultDBPath
	flags.StringVar(&dbPath, "db-path", dbPath, "SQLite 数据库路径")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("多余的参数: %s", strings.Join(flags.Args(), " "))
	}
	db, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.ApplySchema(schema.AllSQL()); err != nil {
		return fmt.Errorf("创建数据库结构失败: %w", err)
	}
	return json.NewEncoder(stdout).Encode(initResult{Module: "callbackdispatcher", Action: "init", Status: "ok", DBPath: dbPath})
}

func printError(stderr io.Writer, err error) {
	_ = json.NewEncoder(stderr).Encode(map[string]string{"error": "init_failed", "message": err.Error()})
}
