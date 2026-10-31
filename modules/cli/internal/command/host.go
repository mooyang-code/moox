package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"time"

	"github.com/spf13/cobra"
)

// hostExitError 把远端命令的退出码带回 moox-cli 的退出码（root.go 的 Execute 识别 ExitCode）。
type hostExitError struct{ code int }

func (e *hostExitError) Error() string { return fmt.Sprintf("远端命令退出码 %d", e.code) }
func (e *hostExitError) ExitCode() int { return e.code }

// openHostRemoteForCommand 是 host 命令装配连接层的入口，测试里替换。
var openHostRemoteForCommand = func(file string) (*hostRemote, error) { return openHostRemote(file, "", nil) }

var hostCmd = &cobra.Command{
	Use:   "host",
	Short: "主机：登记查询、远程执行与文件传输",
}

func init() {
	rootCmd.AddCommand(hostCmd)
	hostCmd.AddCommand(newHostExecCommand(), newHostGetCommand(), newHostPutCommand())
}

func newHostExecCommand() *cobra.Command {
	var file, hostID string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "exec --host <主机 ID> -- <命令> [参数...]",
		Short: "经 SSH 在 moox.toml 登记的主机上执行一条命令",
		Long: "按 moox.toml 里的 SSH 配置连接主机（直连主机，不经 control 中转），执行 -- 之后的命令，标准输出与标准错误原样返回，退出码透传。\n" +
			"命令按参数逐个转义后交给远端 shell，不会做通配符或管道展开；需要管道时写成 sh -c '…'。不接标准输入，不能交互。\n" +
			"这是通用入口：会改动主机的命令，先把完整命令给用户确认。只能出站的主机不能 SSH，用 gateway call 或各模块命令。",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if hostID == "" {
				return errors.New("--host 必填")
			}
			remote, err := openHostRemoteForCommand(file)
			if err != nil {
				return err
			}
			defer remote.Close()
			cmd.SilenceUsage = true
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			result, err := remote.Exec(ctx, hostID, args, nil)
			_, _ = io.WriteString(cmd.OutOrStdout(), result.Stdout)
			_, _ = io.WriteString(cmd.ErrOrStderr(), result.Stderr)
			if err != nil {
				if result.ExitCode != 0 {
					return &hostExitError{code: result.ExitCode}
				}
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&hostID, "host", "", "主机 ID（moox.toml 里的主机名称）")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "命令超时")
	return cmd
}

func newHostGetCommand() *cobra.Command {
	var file, hostID string
	var yes bool
	cmd := &cobra.Command{
		Use:   "get --host <主机 ID> <远端路径> <本地路径>",
		Short: "把主机上的文件下载到本地（本地已存在时需要 --yes 覆盖）",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if hostID == "" {
				return errors.New("--host 必填")
			}
			remotePath, localPath := args[0], args[1]
			if _, err := os.Stat(localPath); err == nil && !yes {
				return writePreview(cmd, map[string]any{"action": "download_overwrite", "host": hostID, "remote": remotePath, "local": localPath, "local_exists": true})
			}
			remote, err := openHostRemoteForCommand(file)
			if err != nil {
				return err
			}
			defer remote.Close()
			cmd.SilenceUsage = true
			var buffer bytes.Buffer
			size, err := remote.Download(cmd.Context(), hostID, remotePath, &buffer)
			if err != nil {
				return err
			}
			if err := os.WriteFile(localPath, buffer.Bytes(), 0o600); err != nil {
				return err
			}
			return printJSON(cmd.OutOrStdout(), map[string]any{"status": "downloaded", "host": hostID, "remote": remotePath, "local": localPath, "bytes": size})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&hostID, "host", "", "主机 ID（moox.toml 里的主机名称）")
	cmd.Flags().BoolVar(&yes, "yes", false, "本地文件已存在时确认覆盖")
	return cmd
}

func newHostPutCommand() *cobra.Command {
	var file, hostID string
	var yes bool
	cmd := &cobra.Command{
		Use:   "put --host <主机 ID> <本地路径> <远端路径>",
		Short: "把本地文件上传到主机（远端已存在时默认只预览，--yes 才覆盖）",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if hostID == "" {
				return errors.New("--host 必填")
			}
			localPath, remotePath := args[0], args[1]
			info, err := os.Stat(localPath)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("%s 不是普通文件", localPath)
			}
			if !path.IsAbs(remotePath) {
				return errors.New("远端路径需要是绝对路径")
			}
			remote, err := openHostRemoteForCommand(file)
			if err != nil {
				return err
			}
			defer remote.Close()
			cmd.SilenceUsage = true
			if !yes {
				if _, err := remote.Exec(cmd.Context(), hostID, []string{"test", "-e", remotePath}, nil); err == nil {
					return writePreview(cmd, map[string]any{"action": "upload_overwrite", "host": hostID, "local": localPath, "remote": remotePath, "remote_exists": true, "bytes": info.Size()})
				}
			}
			source, err := os.Open(localPath)
			if err != nil {
				return err
			}
			defer source.Close()
			if err := remote.Upload(cmd.Context(), hostID, source, info.Size(), remotePath, info.Mode().Perm()); err != nil {
				return err
			}
			return printJSON(cmd.OutOrStdout(), map[string]any{"status": "uploaded", "host": hostID, "local": localPath, "remote": remotePath, "bytes": info.Size()})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&hostID, "host", "", "主机 ID（moox.toml 里的主机名称）")
	cmd.Flags().BoolVar(&yes, "yes", false, "远端已存在时确认覆盖")
	return cmd
}
