package catalog

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
)

// scopedSubjects 是可以改动的标的来源：模拟源数据集的标的在成员加入之后发生变化。
type scopedSubjects struct{ list []string }

func (s *scopedSubjects) ListDatasetSubjects(context.Context, string, string) ([]string, error) {
	return s.list, nil
}

func refService(t *testing.T, subjects *scopedSubjects) *Service {
	t.Helper()
	svc := NewService(openCatalogStore(t), newMetadataFake(), WithRefSubjectSource(subjects))
	_, err := svc.CreateTask(context.Background(), newTask())
	require.NoError(t, err)
	return svc
}

// 加成员时校验 cs_ref 的参考标的在任务的标的范围内，不在就拒绝，错误里写出标的 ID。
func TestAddMemberRejectsReferenceOutsideScope(t *testing.T) {
	ctx := context.Background()
	svc := refService(t, &scopedSubjects{list: []string{"BTC", "ETH"}})
	_, err := svc.CreateFactor(ctx, factorWithSpec("rel_sol", "output: close / cs_ref(close, \"SOL\")\n"))
	require.NoError(t, err, "保存定义时只校验写法，不校验标的范围")
	_, err = svc.AddTaskMember(ctx, testTaskID(), "rel_sol", "", nil)
	require.ErrorContains(t, err, "参考标的 SOL 不在任务的标的范围内")

	_, err = svc.CreateFactor(ctx, factorWithSpec("rel_btc", "output: close / cs_ref(close, \"BTC\")\n"))
	require.NoError(t, err)
	member, err := svc.AddTaskMember(ctx, testTaskID(), "rel_btc", "", nil)
	require.NoError(t, err)
	require.Equal(t, "rel_btc", member.MemberKey)
}

// 启用成员与启用任务时重新校验：参考标的在成员加入之后离开了源数据集，就拒绝启用。
func TestEnableRejectsReferenceThatLeftScope(t *testing.T) {
	ctx := context.Background()
	subjects := &scopedSubjects{list: []string{"BTC", "ETH"}}
	svc := refService(t, subjects)
	_, err := svc.CreateFactor(ctx, factorWithSpec("rel_btc", "output: close / cs_ref(close, \"BTC\")\n"))
	require.NoError(t, err)
	addMember(t, svc, testTaskID(), "rel_btc", domain.MemberStatusDisabled)

	subjects.list = []string{"ETH"}
	_, err = svc.SetTaskMemberStatus(ctx, testTaskID(), "rel_btc", domain.MemberStatusEnabled)
	require.ErrorContains(t, err, "参考标的 BTC 不在任务的标的范围内")

	subjects.list = []string{"BTC", "ETH"}
	_, err = svc.SetTaskMemberStatus(ctx, testTaskID(), "rel_btc", domain.MemberStatusEnabled)
	require.NoError(t, err)
	_, err = svc.SetTaskStatus(ctx, testTaskID(), domain.TaskStatusDisabled)
	require.NoError(t, err)

	subjects.list = []string{"ETH"}
	_, err = svc.SetTaskStatus(ctx, testTaskID(), domain.TaskStatusEnabled)
	require.ErrorContains(t, err, "参考标的 BTC 不在任务的标的范围内")
	task, err := svc.db.GetTask(ctx, testTaskID())
	require.NoError(t, err)
	require.Equal(t, domain.TaskStatusDisabled, task.Status)

	subjects.list = []string{"BTC"}
	_, err = svc.SetTaskStatus(ctx, testTaskID(), domain.TaskStatusEnabled)
	require.NoError(t, err)
}

// 定义里的标的写法不合法时，保存定义就拒绝。
func TestCreateFactorRejectsBadReferenceLiteral(t *testing.T) {
	svc := refService(t, &scopedSubjects{list: []string{"BTC"}})
	_, err := svc.CreateFactor(context.Background(), factorWithSpec("rel_bad", "output: close / cs_ref(close, \"BTC USDT\")\n"))
	require.ErrorContains(t, err, "不允许的字符")
}
