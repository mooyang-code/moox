package command

import (
	"encoding/json"
	"strings"

	"github.com/spf13/cobra"
)

var opsCmd = &cobra.Command{
	Use:   "ops",
	Short: "云资源运维工具",
}

var tencentOpsCmd = &cobra.Command{
	Use:   "tencent",
	Short: "腾讯云运维工具",
}

func init() {
	rootCmd.AddCommand(opsCmd)
	opsCmd.AddCommand(tencentOpsCmd)
}

func writeJSON(cmd *cobra.Command, v any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
