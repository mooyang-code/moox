package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"strings"
	"time"

	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/spf13/cobra"
)

type subjectExclusionFlags struct {
	File      string
	SpaceID   string
	Subjects  []string
	Market    string
	Reason    string
	ExpiresIn time.Duration
}

var (
	subjectExcludeFlags    subjectExclusionFlags
	subjectUnexcludeFlags  subjectExclusionFlags
	subjectExclusionsFlags subjectExclusionFlags
)

var collectorSubjectsExcludeCmd = &cobra.Command{
	Use:   "exclude",
	Short: "把标的从采集计划里临时去掉（交易所公告下架、数据异常等）",
	Long: "排除叠加在标签成员之上，只作用于采集计划：下一轮对账（≤1 分钟）生成新计划，按提前量生效；交接进行中时等交接完成。\n" +
		"标签成员与标的属性照常同步。--market 不填表示该空间所有市场；--expires-in 到期后标的自动回到计划。",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runSubjectExclude(cmd.Context(), cmd.OutOrStdout(), subjectExcludeFlags)
	},
}

var collectorSubjectsUnexcludeCmd = &cobra.Command{
	Use:   "unexclude",
	Short: "解除排除：标的从新的生效周期起回到采集（期间的历史需要时用补采）",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runSubjectUnexclude(cmd.Context(), cmd.OutOrStdout(), subjectUnexcludeFlags)
	},
}

var collectorSubjectsExclusionsCmd = &cobra.Command{
	Use:   "exclusions",
	Short: "列出空间当前生效的排除（设置人、原因、设置与到期时间）",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runSubjectExclusions(cmd.Context(), cmd.OutOrStdout(), subjectExclusionsFlags)
	},
}

func init() {
	collectorSubjectsCmd.AddCommand(collectorSubjectsExcludeCmd, collectorSubjectsUnexcludeCmd, collectorSubjectsExclusionsCmd)
	for _, item := range []struct {
		cmd   *cobra.Command
		flags *subjectExclusionFlags
	}{{collectorSubjectsExcludeCmd, &subjectExcludeFlags}, {collectorSubjectsUnexcludeCmd, &subjectUnexcludeFlags}, {collectorSubjectsExclusionsCmd, &subjectExclusionsFlags}} {
		flags := item.cmd.Flags()
		flags.StringVar(&item.flags.File, "file", "./moox.toml", "部署配置文件；用于解析操作员 SSH 网关")
		flags.StringVar(&item.flags.SpaceID, "space-id", "", "空间 ID（必填）")
		if item.cmd == collectorSubjectsExclusionsCmd {
			continue
		}
		flags.StringSliceVar(&item.flags.Subjects, "subjects", nil, "标的 ID，逗号分隔，例如 BTC-USDT,ETH-USDT")
		flags.StringVar(&item.flags.Market, "market", "", "市场（spot、swap 等）；不填表示该空间所有市场")
	}
	collectorSubjectsExcludeCmd.Flags().StringVar(&subjectExcludeFlags.Reason, "reason", "", "排除原因（必填）")
	collectorSubjectsExcludeCmd.Flags().DurationVar(&subjectExcludeFlags.ExpiresIn, "expires-in", 0, "多久后自动失效，例如 72h；不填表示不过期")
}

// exclusionOperator 是写进排除记录与活动日志的设置人：本机用户与主机名。
func exclusionOperator() string {
	name := "unknown"
	if current, err := user.Current(); err == nil && current.Username != "" {
		name = current.Username
	}
	host, _ := os.Hostname()
	return "moox-cli:" + name + "@" + host
}

func exclusionRequest(flags subjectExclusionFlags) (string, []string, error) {
	spaceID := strings.TrimSpace(flags.SpaceID)
	if spaceID == "" {
		return "", nil, errors.New("--space-id 必填")
	}
	var subjects []string
	for _, subject := range flags.Subjects {
		if subject = strings.TrimSpace(subject); subject != "" {
			subjects = append(subjects, subject)
		}
	}
	if len(subjects) == 0 {
		return "", nil, errors.New("--subjects 至少一个标的")
	}
	return spaceID, subjects, nil
}

func runSubjectExclude(ctx context.Context, out io.Writer, flags subjectExclusionFlags) error {
	spaceID, subjects, err := exclusionRequest(flags)
	if err != nil {
		return err
	}
	if strings.TrimSpace(flags.Reason) == "" {
		return errors.New("--reason 必填")
	}
	if flags.ExpiresIn < 0 {
		return errors.New("--expires-in 不能为负")
	}
	req := &collectorpb.SetSubjectExclusionsReq{SpaceId: spaceID, SubjectIds: subjects, MarketType: strings.TrimSpace(flags.Market), Reason: strings.TrimSpace(flags.Reason), Operator: exclusionOperator()}
	if flags.ExpiresIn > 0 {
		req.ExpiresAt = time.Now().Add(flags.ExpiresIn).UTC().Format(time.RFC3339)
	}
	rsp := &collectorpb.SetSubjectExclusionsRsp{}
	if err := invokeCollector(ctx, flags.File, "SetSubjectExclusions", req, rsp, func() *collectorpb.RetInfo { return rsp.GetRetInfo() }); err != nil {
		return err
	}
	return writeExclusionJSON(out, map[string]any{"status": "success", "note": "下一轮对账（≤1 分钟）生成新计划并按提前量生效；交接进行中时等交接完成", "exclusions": exclusionRows(rsp.GetExclusions())})
}

func runSubjectUnexclude(ctx context.Context, out io.Writer, flags subjectExclusionFlags) error {
	spaceID, subjects, err := exclusionRequest(flags)
	if err != nil {
		return err
	}
	req := &collectorpb.ClearSubjectExclusionsReq{SpaceId: spaceID, SubjectIds: subjects, MarketType: strings.TrimSpace(flags.Market), Operator: exclusionOperator()}
	rsp := &collectorpb.ClearSubjectExclusionsRsp{}
	if err := invokeCollector(ctx, flags.File, "ClearSubjectExclusions", req, rsp, func() *collectorpb.RetInfo { return rsp.GetRetInfo() }); err != nil {
		return err
	}
	return writeExclusionJSON(out, map[string]any{"status": "success", "removed": rsp.GetRemoved()})
}

func runSubjectExclusions(ctx context.Context, out io.Writer, flags subjectExclusionFlags) error {
	spaceID := strings.TrimSpace(flags.SpaceID)
	if spaceID == "" {
		return errors.New("--space-id 必填")
	}
	rsp := &collectorpb.ListSubjectExclusionsRsp{}
	if err := invokeCollector(ctx, flags.File, "ListSubjectExclusions", &collectorpb.ListSubjectExclusionsReq{SpaceId: spaceID}, rsp, func() *collectorpb.RetInfo { return rsp.GetRetInfo() }); err != nil {
		return err
	}
	return writeExclusionJSON(out, map[string]any{"space_id": spaceID, "exclusions": exclusionRows(rsp.GetExclusions())})
}

// invokeCollector 调 CollectMgr 的一个方法，返回码不是成功时报错。
func invokeCollector(ctx context.Context, file, method string, req, rsp any, ret func() *collectorpb.RetInfo) error {
	gateway, err := openCommandGateway(ctx, file, nil)
	if err != nil {
		return err
	}
	defer gateway.Close()
	if err := gateway.Invoke(ctx, "trpc.moox.collector.CollectMgr", method, req, rsp); err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	if info := ret(); info.GetCode() != collectorpb.ErrorCode_SUCCESS {
		return fmt.Errorf("%s: %s (code %d)", method, info.GetMsg(), info.GetCode())
	}
	return nil
}

func exclusionRows(items []*collectorpb.SubjectExclusion) []map[string]string {
	rows := make([]map[string]string, 0, len(items))
	for _, item := range items {
		market := item.GetMarketType()
		if market == "" {
			market = "all"
		}
		rows = append(rows, map[string]string{"subject_id": item.GetSubjectId(), "market": market, "reason": item.GetReason(), "operator": item.GetOperator(), "created_at": item.GetCreatedAt(), "expires_at": item.GetExpiresAt()})
	}
	return rows
}

func writeExclusionJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
