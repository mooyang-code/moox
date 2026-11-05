package command

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/stretchr/testify/require"
)

type fakeStartClient struct {
	tasks     []adminclient.CollectionTaskSummary
	instances []setupStartInstance
	online    bool
	enabled   []string
	triggers  int
	created   int
	strategy  int
	// subjectMisses 是启用任务前还要返回“没有有效标的”的次数；otherErr 非空时直接返回它。
	subjectMisses int
	otherErr      error
	enableCalls   int
	syncs         int
	syncBusy      int
	syncSubjects  int
}

func (f *fakeStartClient) Tasks(context.Context, string) ([]adminclient.CollectionTaskSummary, error) {
	return f.tasks, nil
}
func (f *fakeStartClient) EnableTask(_ context.Context, _, taskID string) error {
	require.Zero(nil2T{}, f.triggers)
	f.enableCalls++
	if f.otherErr != nil {
		return f.otherErr
	}
	if f.subjectMisses > 0 {
		f.subjectMisses--
		return errors.New("UpdateTask rejected: 所选标签没有有效标的")
	}
	f.enabled = append(f.enabled, taskID)
	return nil
}
func (f *fakeStartClient) SyncSubjects(context.Context, string, string) (int, error) {
	f.syncs++
	if f.syncBusy > 0 {
		f.syncBusy--
		return 0, errSubjectSyncBusy
	}
	f.subjectMisses = 0
	return f.syncSubjects, nil
}
func (f *fakeStartClient) StreamOnline(context.Context, string) (bool, error) { return f.online, nil }
func (f *fakeStartClient) Trigger(context.Context, string) error {
	f.triggers++
	return nil
}
func (f *fakeStartClient) PlanState(context.Context, string) (bool, string, error) {
	return true, "", nil
}
func (f *fakeStartClient) Instances(context.Context, string) ([]setupStartInstance, error) {
	return f.instances, nil
}
func (f *fakeStartClient) EnsureStrategy(context.Context, string, string, string) error {
	f.strategy++
	return nil
}
func (f *fakeStartClient) CreateInstance(_ context.Context, instance setupStartInstance) error {
	f.created++
	f.instances = append(f.instances, instance)
	return nil
}
func (f *fakeStartClient) EnableInstance(context.Context, string, string) error { return nil }

// nil2T 让 fake 里的断言失败时直接 panic，测试照样失败。
type nil2T struct{}

func (nil2T) Errorf(format string, args ...any) { panic(format) }
func (nil2T) FailNow()                          { panic("failed") }

func startRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, example := range spaceExamples {
		name := filepath.Join(root, filepath.FromSlash(example.DSL))
		require.NoError(t, os.MkdirAll(filepath.Dir(name), 0o755))
		require.NoError(t, os.WriteFile(name, []byte("name: "+example.StrategyID+"\n"), 0o644))
	}
	return root
}

func startClient() *fakeStartClient {
	return &fakeStartClient{online: true, tasks: []adminclient.CollectionTaskSummary{{TaskID: "a"}, {TaskID: "b"}}}
}

func TestSetupStartEnablesAllTasksThenTriggersOnce(t *testing.T) {
	snapshot := setupSnapshot(t)
	client := startClient()
	summary, err := runSetupStart(context.Background(), setupDeps{}, client, snapshot, t.TempDir(), startRepository(t), "crypto", time.Millisecond)
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, client.enabled)
	require.Equal(t, 1, client.triggers)
	require.Equal(t, 2, summary.TasksEnabled)
	require.Zero(t, client.syncs, "标签已有成员时不额外同步")
}

func TestSetupStartWithoutTasksFails(t *testing.T) {
	_, err := runSetupStart(context.Background(), setupDeps{}, &fakeStartClient{online: true}, setupSnapshot(t), t.TempDir(), t.TempDir(), "crypto", time.Millisecond)
	require.ErrorContains(t, err, "no_collection_tasks")
}

func TestSetupStartStrategyIsRepeatableAndRespectsUserInstance(t *testing.T) {
	repository := startRepository(t)
	client := startClient()
	summary := setupStartSummary{}
	example := spaceExamples["crypto"]
	status, err := setupStartStrategy(context.Background(), client, repository, "crypto", example, &summary)
	require.NoError(t, err)
	require.Equal(t, "created", status)
	status, err = setupStartStrategy(context.Background(), client, repository, "crypto", example, &summary)
	require.NoError(t, err)
	require.Equal(t, "unchanged", status, "重复运行不重复创建")
	require.Equal(t, 1, client.created)

	bound := startClient()
	bound.instances = []setupStartInstance{{InstanceID: "mine", FactorTasks: map[string]string{example.Market: example.FactorTask}}}
	status, err = setupStartStrategy(context.Background(), bound, repository, "crypto", example, &summary)
	require.NoError(t, err)
	require.Equal(t, "skipped", status, "因子任务已被用户实例绑定")
	require.Zero(t, bound.created)
	require.NotEmpty(t, summary.Notes)
}

func TestSetupStartPrerequisitesDependOnSpace(t *testing.T) {
	manifest := setupconfig.Manifest{}
	require.ErrorContains(t, setupStartPrerequisites(manifest, "crypto"), "start_prerequisite_missing")
	require.ErrorContains(t, setupStartPrerequisites(manifest, "stockcn"), "scf_plan")
}

func TestSetupStartEnableTaskRetriesWhileSubjectsNotResolvable(t *testing.T) {
	client := &fakeStartClient{subjectMisses: 2, syncBusy: 1, syncSubjects: 300}
	task := adminclient.CollectionTaskSummary{TaskID: "a", TagIDs: []string{"cn_hs300"}}
	require.NoError(t, setupStartEnableTask(context.Background(), client, "stockcn", task, time.Millisecond))
	require.Equal(t, 3, client.enableCalls, "Collector 忙时下一轮再同步，同步完再启用")
	require.Equal(t, 2, client.syncs)
	require.Equal(t, []string{"a"}, client.enabled)

	empty := &fakeStartClient{subjectMisses: 5}
	require.ErrorContains(t, setupStartEnableTask(context.Background(), empty, "stockcn", task, time.Millisecond), "没有成员")
}

func TestSetupStartEnableTaskDoesNotRetryOtherErrors(t *testing.T) {
	client := &fakeStartClient{otherErr: errors.New("UpdateTask rejected: 其他原因")}
	require.Error(t, setupStartEnableTask(context.Background(), client, "crypto", adminclient.CollectionTaskSummary{TaskID: "a"}, time.Millisecond))
	require.Equal(t, 1, client.enableCalls)
}

func TestSetupStartStockCNExampleUsesItsOwnTask(t *testing.T) {
	repository := startRepository(t)
	client := startClient()
	summary := setupStartSummary{}
	example := spaceExamples["stockcn"]
	status, err := setupStartStrategy(context.Background(), client, repository, "stockcn", example, &summary)
	require.NoError(t, err)
	require.Equal(t, "created", status)
	require.Equal(t, []setupStartInstance{{InstanceID: "default_dual_ma_cn_5m", StrategyID: "default_dual_ma_cn_5m", SpaceID: "stockcn", FactorTasks: map[string]string{"equity": "ftask_cn_5m"}, Enabled: true}}, client.instances)
}
