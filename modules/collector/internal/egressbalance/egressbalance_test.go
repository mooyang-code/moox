package egressbalance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeProber struct {
	ips    map[string]string
	failOn string
	probes int
}

func (p *fakeProber) ProbeEgressIP(_ context.Context, _, instanceID string) (string, error) {
	p.probes++
	if instanceID == p.failOn {
		return "", errors.New("probe failed")
	}
	return p.ips[instanceID], nil
}

var fourFunctions = []string{"f-000", "f-001", "f-002", "f-003"}

func armed(t *testing.T, cfg Config) *Balancer {
	t.Helper()
	b, err := New(cfg)
	require.NoError(t, err)
	b.Arm()
	return b
}

func TestDuplicatesKeepsTheFirstFunctionOfEachIP(t *testing.T) {
	ips := map[string]string{"f-000": "1.1.1.1", "f-001": "1.1.1.1", "f-002": "2.2.2.2", "f-003": "1.1.1.1", "f-004": "2.2.2.2", "f-005": "3.3.3.3"}
	require.Equal(t, []string{"f-001", "f-003", "f-004"}, Duplicates(ips, 1))
	require.Equal(t, []string{"f-003"}, Duplicates(ips, 2), "每个 IP 最多保留 2 个")
	require.Empty(t, Duplicates(map[string]string{"a": "1.1.1.1", "b": "2.2.2.2"}, 1))
	require.Equal(t, Duplicates(ips, 1), Duplicates(ips, 0), "非正数按 1 处理")
}

func TestNextRecreatesSharedIPsUntilTheyAreSpread(t *testing.T) {
	prober := &fakeProber{ips: map[string]string{"f-000": "1.1.1.1", "f-001": "1.1.1.1", "f-002": "1.1.1.1", "f-003": "2.2.2.2"}}
	b := armed(t, Config{})
	ctx, now := context.Background(), time.Now()

	require.Equal(t, []string{"f-001", "f-002"}, b.Next(ctx, prober, "s", fourFunctions, now), "共用 IP 的函数除第一个外都重建")
	require.True(t, b.Pending())

	prober.ips["f-001"], prober.ips["f-002"] = "3.3.3.3", "1.1.1.1" // 重建后一个拿到新 IP，另一个仍然重复
	require.Equal(t, []string{"f-002"}, b.Next(ctx, prober, "s", fourFunctions, now))

	prober.ips["f-002"] = "4.4.4.4"
	require.Empty(t, b.Next(ctx, prober, "s", fourFunctions, now))
	require.False(t, b.Pending(), "每个函数的 IP 都不同：结束")
	probes := prober.probes
	require.Empty(t, b.Next(ctx, prober, "s", fourFunctions, now))
	require.Equal(t, probes, prober.probes, "结束后不再探测")
}

func TestNextStopsWhenTheAddressPoolIsTooSmall(t *testing.T) {
	prober := &fakeProber{ips: map[string]string{"f-000": "1.1.1.1", "f-001": "1.1.1.1", "f-002": "2.2.2.2", "f-003": "2.2.2.2"}}
	b := armed(t, Config{})
	ctx, now := context.Background(), time.Now()
	require.NotEmpty(t, b.Next(ctx, prober, "s", fourFunctions, now), "第一轮：不同 IP 数 2，是目前最好")
	require.NotEmpty(t, b.Next(ctx, prober, "s", fourFunctions, now), "第二轮没有改善，还可以再试一轮")
	require.Empty(t, b.Next(ctx, prober, "s", fourFunctions, now), "连续两轮没有改善：地址池里的 IP 不够，停止")
	require.False(t, b.Pending())
}

func TestNextStopsAfterTheConfiguredRounds(t *testing.T) {
	prober := &fakeProber{ips: map[string]string{"f-000": "1.1.1.1", "f-001": "1.1.1.1", "f-002": "1.1.1.1", "f-003": "1.1.1.1"}}
	b := armed(t, Config{Rounds: 1})
	ctx, now := context.Background(), time.Now()
	require.NotEmpty(t, b.Next(ctx, prober, "s", fourFunctions, now))
	prober.ips["f-001"] = "2.2.2.2" // 有改善，但已经用完轮数
	require.Empty(t, b.Next(ctx, prober, "s", fourFunctions, now))
	require.False(t, b.Pending())
}

func TestNextWaitsForIdleTimeAndCompleteProbes(t *testing.T) {
	prober := &fakeProber{ips: map[string]string{"f-000": "1.1.1.1", "f-001": "1.1.1.1", "f-002": "2.2.2.2", "f-003": "3.3.3.3"}}
	b := armed(t, Config{IdleWindow: Window{Start: "15:10", End: "09:00", Timezone: "Asia/Shanghai"}})
	ctx := context.Background()
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	require.NoError(t, err)
	trading, idle := time.Date(2026, 11, 6, 10, 0, 0, 0, shanghai), time.Date(2026, 11, 6, 16, 0, 0, 0, shanghai)

	require.Empty(t, b.Next(ctx, prober, "s", fourFunctions, trading), "不在空闲时段不删除重建")
	require.Zero(t, prober.probes, "也不探测")

	prober.failOn = "f-003"
	require.Empty(t, b.Next(ctx, prober, "s", fourFunctions, idle), "有函数探测失败：这一轮不做决定")
	require.True(t, b.Pending())
	require.Zero(t, b.rounds, "探测失败不算一轮")

	prober.failOn = ""
	require.Equal(t, []string{"f-001"}, b.Next(ctx, prober, "s", fourFunctions, idle))
}

func TestNextDoesNothingUnlessArmed(t *testing.T) {
	prober := &fakeProber{ips: map[string]string{"f-000": "1.1.1.1", "f-001": "1.1.1.1"}}
	b, err := New(Config{})
	require.NoError(t, err)
	require.Empty(t, b.Next(context.Background(), prober, "s", fourFunctions, time.Now()), "没有新建函数就不探测、不重建")
	require.Zero(t, prober.probes)
	var nilBalancer *Balancer
	require.Empty(t, nilBalancer.Next(context.Background(), prober, "s", fourFunctions, time.Now()), "没配置（nil）就不做")
	require.False(t, nilBalancer.Pending())
}

func TestWindow(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	require.NoError(t, err)
	at := func(h, m int) time.Time { return time.Date(2026, 11, 6, h, m, 0, 0, shanghai) }
	overnight := Window{Start: "15:10", End: "09:00", Timezone: "Asia/Shanghai"}
	require.True(t, overnight.Contains(at(15, 10)))
	require.True(t, overnight.Contains(at(20, 0)))
	require.True(t, overnight.Contains(at(8, 59)))
	require.False(t, overnight.Contains(at(9, 0)))
	require.False(t, overnight.Contains(at(11, 45)))
	require.False(t, overnight.Contains(at(15, 9)))
	require.True(t, overnight.Contains(at(7, 30).UTC()), "按配置的时区换算，与传入时刻的时区无关")

	daytime := Window{Start: "01:00", End: "05:00"}
	require.True(t, daytime.Contains(time.Date(2026, 11, 6, 2, 0, 0, 0, time.UTC)), "起点早于终点：不跨午夜，默认 UTC")
	require.False(t, daytime.Contains(time.Date(2026, 11, 6, 6, 0, 0, 0, time.UTC)))

	require.True(t, Window{}.Contains(time.Now()), "零值全天")
	_, err = New(Config{IdleWindow: Window{Start: "15:10"}})
	require.Error(t, err, "只写一个端点")
	_, err = New(Config{IdleWindow: Window{Start: "25:00", End: "09:00"}})
	require.Error(t, err)
	_, err = New(Config{IdleWindow: Window{Start: "15:10", End: "09:00", Timezone: "Nowhere/Land"}})
	require.Error(t, err)
}
