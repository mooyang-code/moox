package command

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/spf13/cobra"
)

// newFactorImportCommand 按目录批量导入因子定义：读目录下全部 *.yaml（按文件名排序）发给 FactorMgr，
// 解析、校验与写入都在 FactorMgr 侧，整批成功或整批拒绝。
func newFactorImportCommand() *cobra.Command {
	var file, dir string
	cmd := &cobra.Command{
		Use:   "import",
		Short: "按目录批量导入因子定义（目录下全部 *.yaml；任一因子已存在或编译失败时整批拒绝）",
		Long: "读 --dir 目录下全部 *.yaml（每个文件是 [{factor_id, name, category_id, category_name, spec, allow_partial_universe}] 列表，\n" +
			"格式见仓库 modules/factor/factors/），整批发给 FactorMgr：factor_id 在全部文件内必须唯一，任一因子已存在、解析或编译失败时\n" +
			"一个都不写；带 category_name 的分类先建立或更新。只建不改：改已有定义用 factor update；导入后用 factor member add 挂到计算任务。\n\n" +
			"对应接口：trpc.moox.factor.FactorMgr ImportFactorCatalog。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			files, err := readFactorCatalogDir(dir)
			if err != nil {
				return err
			}
			cmd.SilenceUsage = true
			gateway, err := openCommandGateway(cmd.Context(), file, nil)
			if err != nil {
				return err
			}
			defer gateway.Close()
			return runFactorImport(cmd.Context(), gateway, files, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&file, "file", "./moox.toml", "部署配置文件；用于解析操作员 SSH 网关")
	cmd.Flags().StringVar(&dir, "dir", "", "因子库目录（读其中全部 *.yaml）")
	_ = cmd.MarkFlagRequired("dir")
	return cmd
}

// readFactorCatalogDir 读目录下全部 *.yaml，按文件名排序；文件名只带基本名，用于服务端报错定位。
func readFactorCatalogDir(dir string) ([]*factorpb.FactorCatalogFile, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("因子库目录 %s 里没有 *.yaml", dir)
	}
	sort.Strings(paths)
	files := make([]*factorpb.FactorCatalogFile, 0, len(paths))
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("读取因子库 %s：%w", path, err)
		}
		files = append(files, &factorpb.FactorCatalogFile{Name: filepath.Base(path), Content: string(raw)})
	}
	return files, nil
}

func runFactorImport(ctx context.Context, gateway commandGateway, files []*factorpb.FactorCatalogFile, out io.Writer) error {
	rsp := &factorpb.ImportFactorCatalogRsp{}
	if err := gateway.Invoke(ctx, factorMgr, "ImportFactorCatalog", &factorpb.ImportFactorCatalogReq{Files: files}, rsp); err != nil {
		return fmt.Errorf("导入因子库：%w", err)
	}
	if code := rsp.GetRetInfo().GetCode(); code != commonpb.ErrorCode_SUCCESS {
		return fmt.Errorf("导入因子库失败：%s（%s）", rsp.GetRetInfo().GetMsg(), code)
	}
	imported := make([]map[string]string, 0, len(rsp.GetFactors()))
	for _, factor := range rsp.GetFactors() {
		imported = append(imported, map[string]string{"factor_id": factor.GetFactorId(), "category_id": factor.GetCategoryId()})
	}
	return printJSON(out, map[string]any{"ok": true, "imported": imported})
}
