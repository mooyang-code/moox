package command

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/deploy"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/deployscan"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/identitybundle"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/tlstrust"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
)

func newSetupWipeCommand(deps setupDeps) *cobra.Command {
	var file, expect, expectFunctions string
	var yes, cleanFunctions bool
	var source setupSource
	cmd := &cobra.Command{
		Use:   "wipe",
		Short: "卸载：彻底清空 moox.toml 所列主机上的 MooX 部署（默认只预览，不可恢复）",
		Long: `卸载 MooX：逐台停用并删除全部 moox-* 用户服务，杀掉可执行文件在部署根里的全部进程，清空部署根下的内容（保留目录本身），
再删掉这台电脑上的操作员身份。归档文件（archive/）和内置对象存储的数据（data/seaweedfs/）都在部署根下，会一起清掉，预览里单独列出它们的大小。
不带 --yes 只预览；执行要带 --yes --expect <预览里的 digest>，范围变了会拒绝。
云函数默认不清理（--clean-functions 才清）；不在 moox.toml 里的主机也不清理。中途失败后再运行一次会接着清。
预览的 left_behind 列出卸载不会动的东西（防火墙规则、日志服务、SSH 密钥、本机配置等）。卸载后要重新安装用 setup install。`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if yes && expect == "" {
				return errors.New("setup wipe --yes 要带 --expect <预览里的 digest>，先运行一次不带 --yes 的预览")
			}
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			if err := source.resolve(); err != nil {
				return err
			}
			reports, err := deps.scanDeployment(cmd.Context(), snapshot, deployscan.Options{Sizes: true, Wipe: true})
			if err != nil {
				return err
			}
			plan := deployscan.BuildPlan(reports)
			regionCloud := func(region string) (setupFunctionCloud, error) { return deps.regionCloud(snapshot, region) }
			var functions []setupPlanFunction
			if cleanFunctions {
				var unread []string
				if functions, unread, err = listSetupPlanFunctions(cmd.Context(), regionCloud); err != nil {
					return err
				}
				if len(unread) != 0 {
					return fmt.Errorf("functions_unread: 这些地域读不了云函数，没有清除任何东西：%v", unread)
				}
			}
			if !yes {
				preview := map[string]any{"action": "wipe", "route": deployscan.Route(reports), "plan": plan, "left_behind": wipeLeftBehind(snapshot.Manifest, cleanFunctions)}
				if note := wipeTradeNote(snapshot.Manifest, plan); note != "" {
					preview["note"] = note
				}
				if cleanFunctions {
					// 同一云账号可能有其他环境，单列出来让用户确认。
					preview["functions"], preview["functions_digest"] = functions, setupFunctionsDigest(functions)
				}
				return writePreview(cmd, preview)
			}
			if cleanFunctions && expectFunctions != setupFunctionsDigest(functions) {
				return fmt.Errorf("wipe_scope_changed: 云函数清单和预览不一致（现在是 %s），没有清除任何东西；重新预览并让用户确认", setupFunctionsDigest(functions))
			}
			if expect != plan.Digest {
				return fmt.Errorf("wipe_scope_changed: 清除范围和预览不一致（现在是 %s），没有清除任何主机；重新预览并让用户确认", plan.Digest)
			}
			results, err := executeSetupWipe(cmd.Context(), deps, snapshot, plan, source.operatorDir)
			if err != nil {
				return err
			}
			if err := deleteSetupPlanFunctions(cmd.Context(), regionCloud, functions); err != nil {
				return fmt.Errorf("functions_delete_failed: 主机已清除，云函数没有删完：%w。重新预览 setup wipe --clean-functions，确认后用新的摘要接着删", err)
			}
			return writeSetupJSON(cmd, map[string]any{"status": "wiped", "route": deployscan.RouteFresh, "hosts": results})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "moox.toml 的路径")
	cmd.Flags().StringVar(&source.operatorDir, "operator-dir", "", "这台电脑的操作员身份目录（默认 ~/.config/moox）")
	cmd.Flags().BoolVar(&yes, "yes", false, "确认清除，不加只预览")
	cmd.Flags().StringVar(&expect, "expect", "", "预览输出的 digest，范围变了会拒绝")
	cmd.Flags().BoolVar(&cleanFunctions, "clean-functions", false, "同时清理云上的采集计划函数：预览单列，同一云账号可能有其他环境，要用户单独确认")
	cmd.Flags().StringVar(&expectFunctions, "expect-functions", "", "预览输出的 functions_digest，清单变了会拒绝")
	return cmd
}

// wipeLeftBehind 列出卸载不会动的东西，让用户知道还要不要自己处理。
func wipeLeftBehind(manifest *topology.Config, cleanFunctions bool) []string {
	left := []string{
		"各主机的部署根目录本身（" + manifest.Paths.DeployRoot + "，已清空）与部署用户的 linger 设置",
		"setup firewall 在腾讯云加的端口放行规则（轻量应用服务器的防火墙、CVM 的安全组）",
		"腾讯云日志服务（CLS）里 setup init 开通的日志集与日志主题",
		"这台电脑的 MooX SSH 密钥（~/.ssh/moox_ed25519）和各主机 authorized_keys 里它的公钥",
		"这台电脑上的 moox.toml、moox.secrets.toml 与解密它的密钥",
		"系统钥匙串里已信任的管理台根证书（setup trust-browser 或手工信任的）",
	}
	if !cleanFunctions {
		left = append(left, "云上的采集计划函数（moox-fetch-plan- 开头；要删加 --clean-functions）")
	}
	left = append(left, "COS 桶里的云函数代码包（带 --clean-functions 也不删）")
	if manifest.ArchiveObjectStore != nil {
		left = append(left, "[archive_object_store] 配置的外部对象存储里的归档文件")
	}
	return left
}

// wipeTradeNote 在清空范围里有装着 Trade 的主机时给出提醒；只是提示，不检查持仓。
func wipeTradeNote(manifest *topology.Config, plan deployscan.Plan) string {
	var hosts []string
	for _, host := range plan.Hosts {
		if slices.Contains(manifest.Deployments[host.Host], "trade") || slices.Contains(host.Units, deploy.UnitName("trade")) {
			hosts = append(hosts, host.Host)
		}
	}
	if len(hosts) == 0 {
		return ""
	}
	return "主机 " + strings.Join(hosts, "、") + " 上有 Trade：清空会删除 Trade 库，先确认没有持仓和挂单"
}

// executeSetupWipe 并行清空清除范围里的全部主机并确认结果，setup wipe 和 setup install 共用。一台失败时全部重跑，清空脚本可重复执行。
// 全部清空之后删掉这台电脑上的操作员身份（moox-cli 的私钥、控制机根证书与它的指纹）与缓存的管理台证书：重装的控制机会生成新的 CA，moox-cli 的密钥也重新生成。
func executeSetupWipe(ctx context.Context, deps setupDeps, snapshot *setupSnapshot, plan deployscan.Plan, operatorDir string) ([]deployscan.HostResult, error) {
	manifest := snapshot.Manifest
	results := make([]deployscan.HostResult, len(plan.Hosts))
	errs := make([]error, len(plan.Hosts))
	var workers sync.WaitGroup
	for i, host := range plan.Hosts {
		workers.Add(1)
		go func() {
			defer workers.Done()
			results[i], errs[i] = deps.wipeHost(ctx, manifest.HostByID(host.Host), host.Root)
		}()
	}
	workers.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	reports, err := deps.scanDeployment(ctx, snapshot, deployscan.Options{Wipe: true})
	if err != nil {
		return nil, err
	}
	if left := deployscan.BuildPlan(reports); len(left.Hosts) != 0 {
		return nil, fmt.Errorf("wipe_incomplete: 清空后主机 %s 上还有东西，重新运行 setup wipe 会接着清", left.Hosts[0].Host)
	}
	if err := identitybundle.RemoveOperatorIdentity(operatorDir); err != nil {
		return nil, fmt.Errorf("主机已清空，这台电脑上的操作员身份没有删掉：%w", err)
	}
	if err := os.Remove(tlstrust.CAPath(manifest.ControlHost().Address)); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("主机已清空，这台电脑上缓存的管理台证书没有删掉：%w", err)
	}
	return results, nil
}
