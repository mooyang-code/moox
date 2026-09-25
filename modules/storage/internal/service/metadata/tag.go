package metadata

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

const (
	TagModeAuto       = "auto"
	TagModeManual     = "manual"
	TagMemberActive   = "active"
	TagMemberInactive = "inactive"
)

var (
	ErrTagInvalid       = errors.New("invalid tag")
	ErrTagBuiltin       = errors.New("builtin tag cannot be deleted")
	ErrTagReferenced    = errors.New("tag is referenced by datasets")
	ErrTagAutoMembers   = errors.New("auto tag members are maintained by moox-collector-subject")
	ErrTagSnapshotEmpty = errors.New("auto tag snapshot is empty")
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
	GetTag(context.Context, string, string) (*pb.Tag, error)
	ListTags(context.Context, string, *pb.Page) ([]*pb.Tag, *pb.PageResult, error)
	DeleteTag(context.Context, string, string) error
	ListTagMembers(context.Context, TagMemberQuery) ([]*pb.TagMember, *pb.PageResult, error)
	AddTagMembers(context.Context, string, string, []string) (int, error)
	RemoveTagMembers(context.Context, string, string, []string) (int, error)
	SetTagMemberStatus(context.Context, string, string, []string, string) (int, error)
	ApplyTagSnapshot(context.Context, string, string, time.Time, []*pb.TagSnapshotItem) (TagSnapshotResult, error)
	ReportTagRunFailure(context.Context, string, string, time.Time, string) error
	UpdateSubjectAttributes(context.Context, string, []*pb.SubjectAttributes) (int, int, error)
	ResolveSubjects(context.Context, string, []string) ([]*pb.Subject, error)
}
