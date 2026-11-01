package installflow

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/deployscan"
	"github.com/stretchr/testify/require"
)

func TestRunKeepsDependencyOrderAndSameHostSerial(t *testing.T) {
	steps := []deployscan.Step{
		{Group: 1, Host: "control", Command: "admin"},
		{Group: 2, Host: "storage", Command: "storage-1"},
		{Group: 2, Host: "storage", Command: "storage-2"},
		{Group: 2, Host: "overseas", Command: "overseas-1"},
		{Group: 3, Host: "control", Command: "control"},
	}
	var mu sync.Mutex
	finished := []string{}
	var output bytes.Buffer
	err := Run(context.Background(), steps, func(_ context.Context, step deployscan.Step) error {
		time.Sleep(5 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		finished = append(finished, step.Command)
		return nil
	}, NewProgress(&output))
	require.NoError(t, err)
	position := func(name string) int {
		for i, item := range finished {
			if item == name {
				return i
			}
		}
		return -1
	}
	require.Zero(t, position("admin"))
	require.Less(t, position("storage-1"), position("storage-2"), "同一主机串行")
	require.Equal(t, 4, position("control"), "组间串行")
	require.Contains(t, output.String(), `"event":"step_done"`)
}

func TestRunStopsLaterGroupsAndOtherLanesAfterFailure(t *testing.T) {
	steps := []deployscan.Step{
		{Group: 1, Host: "a", Command: "a-1"},
		{Group: 1, Host: "b", Command: "b-1"},
		{Group: 1, Host: "b", Command: "b-2"},
		{Group: 2, Host: "a", Command: "later"},
	}
	ran := sync.Map{}
	err := Run(context.Background(), steps, func(_ context.Context, step deployscan.Step) error {
		ran.Store(step.Command, true)
		if step.Command == "a-1" {
			return errors.New("boom")
		}
		time.Sleep(50 * time.Millisecond)
		return nil
	}, NewProgress(&bytes.Buffer{}))
	require.ErrorContains(t, err, "boom")
	_, later := ran.Load("later")
	require.False(t, later, "失败后不进入下一组")
	_, next := ran.Load("b-2")
	require.False(t, next, "其他主机做完手上这一步就停")
}

func TestProgressWritesOneJSONPerLine(t *testing.T) {
	var output bytes.Buffer
	progress := NewProgress(&output)
	progress.Event("prepare", map[string]any{"host": "control"})
	require.Equal(t, 1, strings.Count(output.String(), "\n"))
}
