package clsreporter

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	cls "github.com/tencentcloud/tencentcloud-cls-sdk-go"
)

type fakeSender struct{ logs []*cls.Log }

func (s *fakeSender) SendLogList(_ context.Context, _ string, logs []*cls.Log) error {
	s.logs = append([]*cls.Log(nil), logs...)
	return nil
}

func TestReportClientCopiesConcurrentEntries(t *testing.T) {
	sender := &fakeSender{}
	reporter := &reportClient{topicID: "topic", sender: sender}
	var group sync.WaitGroup
	for index := 0; index < 64; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			fields := map[string]string{"symbol": "BTC-USDT", "index": string(rune(index))}
			reporter.Report(Entry{Timestamp: time.Unix(1, 0), Fields: fields})
			fields["symbol"] = "mutated"
		}(index)
	}
	group.Wait()
	require.NoError(t, reporter.Flush(context.Background()))
	require.Len(t, sender.logs, 64)
	for _, item := range sender.logs {
		values := make(map[string]string, len(item.GetContents()))
		for _, content := range item.GetContents() {
			values[content.GetKey()] = content.GetValue()
		}
		require.Equal(t, "BTC-USDT", values["symbol"])
	}
}
