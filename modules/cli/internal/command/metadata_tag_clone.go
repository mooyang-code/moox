package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	commonpb "github.com/mooyang-code/moox/packages/commonpb"
	"github.com/spf13/cobra"
)

type metadataTagCloneFlags struct {
	File    string
	SpaceID string
	From    string
	TagID   string
	Name    string
	Limit   int
}

var metadataTagCloneFlagsValue metadataTagCloneFlags

var metadataTagCmd = &cobra.Command{Use: "tag", Short: "标签维护"}

var metadataTagCloneCmd = &cobra.Command{
	Use:   "clone",
	Short: "由已有标签的前 N 个启用成员创建一个手动标签（用于 canary 等小规模验证）",
	Long: "新标签是手动标签，数据来源与市场类型与源标签一致（创建后不可改）；成员取源标签中启用的标的，按标的 ID 排序后的前 N 个。\n" +
		"标签已存在时只补齐缺少的成员，不删除多余成员。",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runMetadataTagClone(cmd.Context(), cmd.OutOrStdout(), metadataTagCloneFlagsValue)
	},
}

func init() {
	metadataCmd.AddCommand(metadataTagCmd)
	metadataTagCmd.AddCommand(metadataTagCloneCmd)
	flags := metadataTagCloneCmd.Flags()
	flags.StringVar(&metadataTagCloneFlagsValue.File, "manifest", "./moox.toml", "操作员 SSH 网关使用的部署配置文件")
	flags.StringVar(&metadataTagCloneFlagsValue.SpaceID, "space-id", "", "空间 ID")
	flags.StringVar(&metadataTagCloneFlagsValue.From, "from", "", "源标签 ID")
	flags.StringVar(&metadataTagCloneFlagsValue.TagID, "tag-id", "", "新标签 ID")
	flags.StringVar(&metadataTagCloneFlagsValue.Name, "name", "", "新标签名称（默认与 ID 相同）")
	flags.IntVar(&metadataTagCloneFlagsValue.Limit, "limit", 50, "取源标签的前 N 个启用成员")
}

func runMetadataTagClone(ctx context.Context, out io.Writer, flags metadataTagCloneFlags) error {
	if flags.SpaceID == "" || flags.From == "" || flags.TagID == "" || flags.Limit < 1 {
		return errors.New("--space-id, --from, --tag-id and a positive --limit are required")
	}
	gateway, err := openCommandGateway(ctx, flags.File, nil)
	if err != nil {
		return err
	}
	defer gateway.Close()
	const service = "trpc.moox.storage.Metadata"
	source := &pb.GetTagRsp{}
	if err := postStorageRaw(ctx, gateway, service, "GetTag", &pb.GetTagReq{SpaceId: flags.SpaceID, TagId: flags.From}, source); err != nil {
		return fmt.Errorf("读取源标签失败: %w", err)
	}
	if source.GetTag() == nil {
		return fmt.Errorf("源标签 %s 不存在", flags.From)
	}
	var subjects []string
	for page := uint32(1); ; page++ {
		rsp := &pb.ListTagMembersRsp{}
		if err := postStorageRaw(ctx, gateway, service, "ListTagMembers", &pb.ListTagMembersReq{SpaceId: flags.SpaceID, TagId: flags.From, Status: "active", Page: &commonpb.Page{Page: page, Size: 500}}, rsp); err != nil {
			return fmt.Errorf("读取源标签成员失败: %w", err)
		}
		for _, member := range rsp.GetMembers() {
			if id := strings.TrimSpace(member.GetSubject().GetSubjectId()); id != "" {
				subjects = append(subjects, id)
			}
		}
		if !rsp.GetPageResult().GetHasMore() {
			break
		}
	}
	sort.Strings(subjects)
	if len(subjects) > flags.Limit {
		subjects = subjects[:flags.Limit]
	}
	name := flags.Name
	if name == "" {
		name = flags.TagID
	}
	tag := &pb.Tag{SpaceId: flags.SpaceID, TagId: flags.TagID, TagName: name, Description: "由 " + flags.From + " 克隆的手动标签", Mode: "manual", Source: source.GetTag().GetSource(), MarketType: source.GetTag().GetMarketType()}
	if err := postStorageRaw(ctx, gateway, service, "UpsertTag", &pb.UpsertTagReq{Tag: tag, CreateOnly: true}, &pb.UpsertTagRsp{}); err != nil {
		// 已存在时 create_only 会失败；继续补齐成员。
		fmt.Fprintf(out, "UpsertTag: %v\n", err)
	}
	added := &pb.TagMembersRsp{}
	if err := postStorageRaw(ctx, gateway, service, "AddTagMembers", &pb.TagMembersReq{SpaceId: flags.SpaceID, TagId: flags.TagID, SubjectIds: subjects}, added); err != nil {
		return fmt.Errorf("添加标签成员失败: %w", err)
	}
	return printJSON(out, map[string]any{"tag_id": flags.TagID, "source": tag.Source, "market_type": tag.MarketType, "members": len(subjects), "affected": added.GetAffected()})
}
