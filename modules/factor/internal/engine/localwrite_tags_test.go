package engine

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
)

const importSpotTag = "venue:binance|market:spot|source:spot_http"

func taggedWireRequest(tag string, minutes ...int) *factorpb.WriteSeriesReq {
	req := wireRequest(minutes...)
	for i := range req.Tags {
		req.Tags[i] = tag
	}
	return req
}

func storedTags(t *testing.T, store *seriesstore.Store) map[string]int {
	t.Helper()
	src := seriesstore.Source{SpaceID: "crypto", DatasetID: "spot", Freq: "1m"}
	got, err := store.ReadRange(context.Background(), src, []string{"BTC"}, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 1, 1, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	counts := map[string]int{}
	if cols := got["BTC"]; cols != nil {
		for _, tag := range cols.Tags {
			counts[tag]++
		}
	}
	return counts
}

// 导入整段替换：换一个 tag 再导入，标的原有的序列被这一批替换，不会留下旧 tag 的行。
func TestLocalWriteReplacesTheWholeRange(t *testing.T) {
	w, store := newTestWriter(t)
	out := post(t, w, taggedWireRequest(importSpotTag, 0, 1, 2))
	require.Equal(t, commonpb.ErrorCode_SUCCESS, out.GetRetInfo().GetCode(), out.GetRetInfo().GetMsg())
	out = post(t, w, taggedWireRequest("venue:binance|market:spot|source:other", 0, 1, 2))
	require.Equal(t, commonpb.ErrorCode_SUCCESS, out.GetRetInfo().GetCode(), out.GetRetInfo().GetMsg())
	require.Equal(t, map[string]int{"venue:binance|market:spot|source:other": 3}, storedTags(t, store), "同一市场的另一个 tag 整段替换")

	// 无 tag（默认序列）的导入同样整段替换。
	out = post(t, w, wireRequest(0, 1, 2))
	require.Equal(t, commonpb.ErrorCode_SUCCESS, out.GetRetInfo().GetCode(), out.GetRetInfo().GetMsg())
	require.Equal(t, map[string]int{"": 3}, storedTags(t, store))
}
