package command

import (
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/configinit"
	"github.com/spf13/cobra"
)

// runConfigInit 是录入页的入口，测试里替换。
var runConfigInit = configinit.Run

func newConfigInitCommand() *cobra.Command {
	var output string
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "init",
		Short: "在本机打开一次性表单，填写部署信息，生成 moox.toml",
		Long: `在本机起一个只监听 127.0.0.1 的一次性页面，并用系统浏览器打开。
页面分步填写：腾讯云密钥（先验证）、服务器（数据存储、管理台、可选的海外服务器与因子机）、管理员与告警。
腾讯云服务器只填公网 IP 和 SSH 登录方式，地域与规格按 IP 从当前账号识别，不在账号下的 IP 不能使用；
因子机是局域网或家里的一台 Linux 机器，只出站连接，提交前要通过环境检测。各服务按服务器用途自动部署，
确认后直接生成加密保存的 moox.toml（权限 0600，解密密钥存放在系统钥匙串）。

填写的内容只在本机处理，不会经过 Agent：命令只输出「已生成」、文件路径和主机 ID。
如果在 Agent 里运行，请在弹出的浏览器页面里填写，不要把密码贴到对话里。

目标文件已存在时不会覆盖：请先自行改名备份旧文件，或用 --output 指定新的路径。
setup 系列命令只认仓库根目录下名为 moox.toml 的文件，生成到别处的文件需要自己放到这个位置。
页面超过 --timeout（默认 1 小时，页面上有倒计时）没有提交会自动关闭，不写任何文件。
示例里的其余配置（保留期、日志、因子等）保持默认值，之后用 moox-cli config edit 查看和修改。`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := runConfigInit(cmd.Context(), configinit.Options{Output: output, Timeout: timeout, Notice: cmd.ErrOrStderr()})
			if err != nil {
				return err
			}
			return writeSetupJSON(cmd, map[string]any{
				"status": "written", "path": result.Path, "hosts": result.Hosts,
				"next": "确认文件位于仓库根目录且名为 moox.toml（其他 setup 命令只认这个位置），再运行 moox-cli setup validate",
			})
		},
	}
	cmd.Flags().StringVar(&output, "output", defaultSetupFile, "生成的 moox.toml 路径；已存在时拒绝覆盖")
	cmd.Flags().DurationVar(&timeout, "timeout", configinit.DefaultTimeout, "页面有效期，超时自动关闭")
	return cmd
}
