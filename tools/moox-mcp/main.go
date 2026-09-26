package main

import (
	"context"
	"crypto/subtle"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	addr := flag.String("addr", "0.0.0.0:18765", "listen address")
	root := flag.String("root", "/home/ubuntu/go/src/github.com/mooyang-code/moox", "project root")
	tokenFile := flag.String("token-file", "", "file containing the bearer token")
	flag.Parse()

	token, err := readToken(*tokenFile)
	if err != nil {
		log.Fatal(err)
	}
	ws, err := newWorkspace(*root)
	if err != nil {
		log.Fatal(err)
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "moox-mcp", Version: "0.1.0"}, nil)
	registerTools(server, ws)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	mux := http.NewServeMux()
	mux.Handle("/mcp", requireBearer(token, handler))

	log.Printf("moox-mcp listening on %s root %s", *addr, ws.root)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

func readToken(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("--token-file is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("token file is empty")
	}
	return token, nil
}

func requireBearer(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got == r.Header.Get("Authorization") || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func registerTools(server *mcp.Server, ws workspace) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_dir",
		Description: "列出 moox 项目目录中的文件和子目录。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		Path string `json:"path" jsonschema:"相对项目根的目录，空表示根目录"`
	}) (*mcp.CallToolResult, any, error) {
		dir, err := ws.resolve(in.Path)
		if err != nil {
			return toolError(err), nil, nil
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return toolError(err), nil, nil
		}
		var b strings.Builder
		for _, entry := range entries {
			kind := "file"
			if entry.IsDir() {
				kind = "dir"
			}
			info, err := entry.Info()
			if err != nil {
				fmt.Fprintf(&b, "%s\t%s\n", kind, entry.Name())
				continue
			}
			fmt.Fprintf(&b, "%s\t%d\t%s\n", kind, info.Size(), entry.Name())
		}
		return textResult(b.String()), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "read_file",
		Description: "读取 moox 项目中的文本文件。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		Path   string `json:"path" jsonschema:"相对项目根的文件路径"`
		Offset int    `json:"offset,omitempty" jsonschema:"从 0 开始的字节偏移"`
		Limit  int    `json:"limit,omitempty" jsonschema:"最多读取的字节数，默认并最多 8MiB"`
	}) (*mcp.CallToolResult, any, error) {
		path, err := ws.resolve(in.Path)
		if err != nil {
			return toolError(err), nil, nil
		}
		file, err := os.Open(path)
		if err != nil {
			return toolError(err), nil, nil
		}
		defer file.Close()
		limit := in.Limit
		if limit <= 0 || limit > maxFileBytes {
			limit = maxFileBytes
		}
		if in.Offset > 0 {
			if _, err := file.Seek(int64(in.Offset), io.SeekStart); err != nil {
				return toolError(err), nil, nil
			}
		}
		buf := make([]byte, limit)
		n, err := file.Read(buf)
		if err != nil && err != io.EOF {
			return toolError(err), nil, nil
		}
		return textResult(string(buf[:n])), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "write_file",
		Description: "写入或覆盖 moox 项目中的文件，必要时创建父目录。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		Path    string `json:"path" jsonschema:"相对项目根的文件路径"`
		Content string `json:"content" jsonschema:"文件完整内容"`
	}) (*mcp.CallToolResult, any, error) {
		if len(in.Content) > maxFileBytes {
			return toolError(fmt.Errorf("content exceeds %d bytes", maxFileBytes)), nil, nil
		}
		path, err := ws.resolve(in.Path)
		if err != nil {
			return toolError(err), nil, nil
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return toolError(err), nil, nil
		}
		if err := os.WriteFile(path, []byte(in.Content), 0o644); err != nil {
			return toolError(err), nil, nil
		}
		return textResult("wrote " + in.Path), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "edit_file",
		Description: "替换 moox 项目文件中的一段文本。默认要求 old_string 只出现一次。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		Path       string `json:"path" jsonschema:"相对项目根的文件路径"`
		OldString  string `json:"old_string" jsonschema:"要被替换的原文"`
		NewString  string `json:"new_string" jsonschema:"替换后的文本"`
		ReplaceAll bool   `json:"replace_all,omitempty" jsonschema:"为 true 时替换全部匹配"`
	}) (*mcp.CallToolResult, any, error) {
		if in.OldString == "" {
			return toolError(fmt.Errorf("old_string is required")), nil, nil
		}
		path, err := ws.resolve(in.Path)
		if err != nil {
			return toolError(err), nil, nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return toolError(err), nil, nil
		}
		text := string(data)
		count := strings.Count(text, in.OldString)
		if count == 0 {
			return toolError(fmt.Errorf("old_string not found")), nil, nil
		}
		if !in.ReplaceAll && count != 1 {
			return toolError(fmt.Errorf("old_string matched %d times", count)), nil, nil
		}
		updated := strings.ReplaceAll(text, in.OldString, in.NewString)
		if !in.ReplaceAll {
			updated = strings.Replace(text, in.OldString, in.NewString, 1)
		}
		if len(updated) > maxFileBytes {
			return toolError(fmt.Errorf("edited file exceeds %d bytes", maxFileBytes)), nil, nil
		}
		if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
			return toolError(err), nil, nil
		}
		return textResult(fmt.Sprintf("updated %s matches=%d", in.Path, count)), nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "run_shell",
		Description: "在 106.53.107.122 上执行 shell 命令。工作目录默认是 moox 项目根，也可以指定该机器上的其他目录。",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
		Command        string `json:"command" jsonschema:"交给 bash -c 执行的命令"`
		Cwd            string `json:"cwd,omitempty" jsonschema:"工作目录，默认 moox 项目根"`
		TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"超时秒数，默认 60，最大 300"`
	}) (*mcp.CallToolResult, shellResult, error) {
		if strings.TrimSpace(in.Command) == "" {
			return toolError(fmt.Errorf("command is required")), shellResult{}, nil
		}
		dir := ws.root
		if strings.TrimSpace(in.Cwd) != "" {
			info, err := os.Stat(in.Cwd)
			if err != nil {
				return toolError(err), shellResult{}, nil
			}
			if !info.IsDir() {
				return toolError(fmt.Errorf("cwd is not a directory")), shellResult{}, nil
			}
			dir = in.Cwd
		}
		result := runShell(ctx, in.Command, dir, time.Duration(in.TimeoutSeconds)*time.Second)
		out := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: formatShell(result)}}}
		if result.ExitCode != 0 {
			out.IsError = true
		}
		return out, result, nil
	})
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func toolError(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
	}
}
