package command

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/deploy"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/identitybundle"
	"github.com/spf13/cobra"
)

// setupOperatorOutput 是 setup operator 的输出。
type setupOperatorOutput struct {
	Status    string `json:"status"`
	KeyFile   string `json:"key_file"`
	PublicKey string `json:"public_key"`
	// Created 为真表示这次新生成了私钥；Registered 为真表示已经在控制机上登记（替换）了公钥。
	Created    bool   `json:"created"`
	Registered bool   `json:"registered"`
	Note       string `json:"note,omitempty"`
}

// defaultSetupEnsureOperatorKey 确保这台电脑上有 moox-cli 的签名私钥：已有就原样复用，没有才生成。返回公钥。
func defaultSetupEnsureOperatorKey(directory string) (publicKey string, created bool, err error) {
	key, created, err := identitybundle.EnsureSigningKeyFile(filepath.Join(directory, identitybundle.OperatorKeyFile))
	if err != nil {
		return "", false, err
	}
	return identitybundle.PublicKey(key), created, nil
}

// defaultSetupRegisterIdentity 经 SSH 在控制机上登记（替换）一个不属于主机的身份的公钥。
func defaultSetupRegisterIdentity(ctx context.Context, snapshot *setupSnapshot, identity, publicKey string) error {
	control, err := dialSetupHost(ctx, snapshot.Manifest.ControlHost())
	if err != nil {
		return fmt.Errorf("连接控制机: %w", err)
	}
	defer control.Close()
	return deploy.RegisterIdentity(ctx, control, snapshot.Manifest, identity, publicKey)
}

func newSetupOperatorCommand(deps setupDeps) *cobra.Command {
	var file string
	var register bool
	var source setupSource
	cmd := &cobra.Command{
		Use:   "operator",
		Short: "准备这台电脑的操作身份：生成 moox-cli 的签名私钥；--register 把公钥登记到已经装好的控制机",
		Long: `moox-cli 用这台电脑上的一把 ed25519 私钥（~/.config/moox/moox-cli.key）给发往各主机网关的请求签名，控制机只登记公钥。
不带参数：本机已有私钥就原样复用，没有才生成；不连任何主机。全新安装时 setup install 的第一步就是它，公钥在部署控制机时登记。
--register：经 SSH 在控制机上把这台电脑的公钥登记为 moox-cli 的公钥，用于已经装好的系统换操作电脑。它会替换原来登记的公钥，
原来那台电脑随即不能再用（两台电脑不能同时用）。能 SSH 上控制机就能登记。各主机网关在几秒内认新公钥。`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := source.resolveOperatorDir(); err != nil {
				return err
			}
			public, created, err := deps.ensureOperatorKey(source.operatorDir)
			if err != nil {
				return err
			}
			output := setupOperatorOutput{Status: "ready", KeyFile: filepath.Join(source.operatorDir, identitybundle.OperatorKeyFile), PublicKey: public, Created: created}
			if register {
				snapshot, err := deps.load(file)
				if err != nil {
					return err
				}
				defer clearSetupSecrets(snapshot)
				if err := deps.registerIdentity(cmd.Context(), snapshot, "moox-cli", public); err != nil {
					return err
				}
				output.Registered = true
				output.Note = "已替换控制机上登记的 moox-cli 公钥，各主机网关约 10 秒内生效；原来登记的那台电脑不能再用。这台电脑还没有控制机根证书时，再运行一次 moox-cli setup deploy --host " + snapshot.Manifest.ControlHost().Name
			}
			return writeSetupJSON(cmd, output)
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "moox.toml 的路径（只有 --register 要读）")
	cmd.Flags().BoolVar(&register, "register", false, "经 SSH 在控制机上登记这台电脑的公钥（替换原来登记的）")
	cmd.Flags().StringVar(&source.operatorDir, "operator-dir", "", "这台电脑的操作员身份目录（默认 ~/.config/moox）")
	return cmd
}
