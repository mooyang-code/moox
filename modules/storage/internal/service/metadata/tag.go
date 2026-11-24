package metadata

import (
	"context"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/storeerr"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

const (
	TagModeAuto       = "auto"
	TagModeManual     = "manual"
	TagMemberActive   = "active"
	TagMemberInactive = "inactive"
)

// 标签成员的交易状态（t_subject_tags.c_trading_status）：只有 trading 的成员算可交易，ResolveSubjects 与 ListDatasetSubjects
// 的活跃判断都要求它；suspended 是停牌（可能恢复），delisted 是退市。
const (
	TradingStatusTrading   = "trading"
	TradingStatusSuspended = "suspended"
	TradingStatusDelisted  = "delisted"
)

// ValidTradingStatus 报告取值是否是三种交易状态之一。
func ValidTradingStatus(status string) bool {
	switch status {
	case TradingStatusTrading, TradingStatusSuspended, TradingStatusDelisted:
		return true
	}
	return false
}

var (
	ErrTagInvalid       = storeerr.New(storeerr.Invalid, "invalid tag")
	ErrTagBuiltin       = storeerr.New(storeerr.Invalid, "builtin tag cannot be deleted")
	ErrTagReferenced    = storeerr.New(storeerr.Invalid, "tag is referenced by datasets")
	ErrTagAutoMembers   = storeerr.New(storeerr.Invalid, "auto tag members are maintained by moox-collector")
	ErrTagSnapshotEmpty = storeerr.New(storeerr.Invalid, "auto tag snapshot is empty")
)

type TagReferencedError struct {
	TagID      string
	References []*pb.TagReference
}

func (e *TagReferencedError) Error() string {
	ids := make([]string, 0, len(e.References))
	for _, ref := range e.References {
		ids = append(ids, ref.GetDatasetId())
	}
	return fmt.Sprintf("tag %s is referenced by datasets: %s", e.TagID, strings.Join(ids, ","))
}

func (e *TagReferencedError) Is(target error) bool { return target == ErrTagReferenced }

// StoreErrorKind 与 ErrTagReferenced 一样是参数错误。
func (e *TagReferencedError) StoreErrorKind() storeerr.Kind { return storeerr.Invalid }

type TagMemberQuery struct {
	SpaceID string
	TagID   string
	Status  string
	Keyword string
	Page    *pb.Page
}

type TagSnapshotResult struct {
	Added       int
	Activated   int
	Inactivated int
}

type TagStore interface {
	UpsertTag(context.Context, *pb.Tag) (*pb.Tag, error)
	CreateTag(context.Context, *pb.Tag) (*pb.Tag, error)
	GetTag(context.Context, string, string) (*pb.Tag, error)
	ListTags(context.Context, string, *pb.Page) ([]*pb.Tag, *pb.PageResult, error)
	DeleteTag(context.Context, string, string) error
	ListTagMembers(context.Context, TagMemberQuery) ([]*pb.TagMember, *pb.PageResult, error)
	AddTagMembers(context.Context, string, string, []string) (int, error)
	RemoveTagMembers(context.Context, string, string, []string) (int, error)
	SetTagMemberStatus(context.Context, string, string, []string, string) (int, error)
	ApplyTagSnapshot(context.Context, string, string, []*pb.TagSnapshotItem) (TagSnapshotResult, error)
	UpdateSubjectAttributes(context.Context, string, []*pb.SubjectAttributes) (int, int, error)
	// SetTagMemberTradingStatus 只改成员的交易状态，值没变的不写；返回改动数与跳过数（值没变或不是成员）。
	SetTagMemberTradingStatus(context.Context, string, string, []*pb.TagMemberTradingStatus) (int, int, error)
	ResolveSubjects(context.Context, string, []string) ([]*pb.Subject, error)
}
