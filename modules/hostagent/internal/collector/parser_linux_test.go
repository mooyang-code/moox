//go:build linux

package collector

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCPUStat(t *testing.T) {
	got, err := ParseCPUStat([]byte("cpu  10 2 3 40 5 6 7 8 9 10\ncpu0 1 1 1 1\n"))
	require.NoError(t, err)
	assert.Equal(t, CPUStat{
		User: 10, Nice: 2, System: 3, Idle: 40, IOWait: 5,
		IRQ: 6, SoftIRQ: 7, Steal: 8,
	}, got)
}

func TestParseCPUStatRejectsShortAndInvalidInput(t *testing.T) {
	for _, input := range []string{
		"cpu 1 2 3\n",
		"cpu 1 2 nope 4 5 6 7 8\n",
		"intr 1 2 3 4 5 6 7 8\n",
	} {
		_, err := ParseCPUStat([]byte(input))
		assert.Error(t, err, input)
	}
}

func TestParseMeminfo(t *testing.T) {
	total, available, err := ParseMeminfo([]byte(
		"MemTotal:       1024 kB\nMemFree: 1 kB\nMemAvailable: 256 kB\n",
	))
	require.NoError(t, err)
	assert.Equal(t, uint64(1024*1024), total)
	assert.Equal(t, uint64(256*1024), available)
}

func TestParseMeminfoRejectsMissingInvalidAndImpossibleValues(t *testing.T) {
	for _, input := range []string{
		"MemTotal: 1024 kB\n",
		"MemTotal: nope kB\nMemAvailable: 2 kB\n",
		"MemTotal: 1 kB\nMemAvailable: 2 kB\n",
	} {
		_, _, err := ParseMeminfo([]byte(input))
		assert.Error(t, err, input)
	}
}

func TestParseDiskStats(t *testing.T) {
	got, err := ParseDiskStats([]byte(
		"   8       0 sda 10 2 30 4 50 6 70 8 9 100 11 12 13 14\n",
	))
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, DiskStat{
		Name: "sda", ReadOps: 10, ReadSectors: 30,
		WriteOps: 50, WriteSectors: 70, IOTimeMS: 100,
	}, got[0])
}

func TestParseDiskStatsRejectsShortAndInvalidLines(t *testing.T) {
	for _, input := range []string{
		"8 0 sda 1 2\n",
		"8 0 sda nope 2 3 4 5 6 7 8 9 10\n",
	} {
		_, err := ParseDiskStats([]byte(input))
		assert.Error(t, err, input)
	}
}

func TestParseNetworkStats(t *testing.T) {
	input := "Inter-| Receive | Transmit\n face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed\n" +
		"eth0: 100 2 3 4 5 6 7 8 900 10 11 12 13 14 15 16\n"
	got, err := ParseNetworkStats([]byte(input))
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, NetworkStat{
		Name: "eth0", ReceiveBytes: 100, ReceiveErrors: 3, ReceiveDropped: 4,
		TransmitBytes: 900, TransmitErrors: 11, TransmitDropped: 12,
	}, got[0])
}

func TestParseNetworkStatsRejectsShortAndInvalidLines(t *testing.T) {
	for _, input := range []string{
		"Inter-| Receive | Transmit\n face | fields\neth0: 1 2 3\n",
		"Inter-| Receive | Transmit\n face | fields\neth0: nope 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16\n",
	} {
		_, err := ParseNetworkStats([]byte(input))
		assert.Error(t, err, input)
	}
}

func TestParseMounts(t *testing.T) {
	got, err := ParseMounts([]byte(
		"/dev/sda1 / ext4 rw,relatime 0 0\n/dev/sdb1 /data\\040disk xfs ro 0 0\n",
	))
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, Mount{Device: "/dev/sdb1", Mountpoint: "/data disk", FSType: "xfs", ReadOnly: true}, got[1])
}

func TestParseMountsRejectsShortLines(t *testing.T) {
	_, err := ParseMounts([]byte("/dev/sda1 /\n"))
	assert.Error(t, err)
}

func TestCollectorRequiredFailureRejectsSnapshot(t *testing.T) {
	c := testLinuxCollector(map[string]string{
		"/proc/stat":    "cpu 1 2 nope 4 5 6 7 8\n",
		"/proc/meminfo": "MemTotal: 1024 kB\nMemAvailable: 512 kB\n",
	})

	snapshot, statuses, err := c.Collect(context.Background())
	assert.ErrorContains(t, err, "required host collectors failed")
	assert.Nil(t, snapshot)
	require.Len(t, statuses, 2)
	assert.False(t, statuses[0].GetSuccess())
	assert.True(t, statuses[1].GetSuccess())
}

func TestCollectorOptionalFailuresPreserveCPUAndMemory(t *testing.T) {
	c := testLinuxCollector(map[string]string{
		"/proc/stat":      "cpu 1 2 3 40 5 6 7 8\n",
		"/proc/meminfo":   "MemTotal: 1024 kB\nMemAvailable: 512 kB\n",
		"/proc/diskstats": "8 0 sda 1\n",
		"/proc/net/dev":   "eth0: 1 2\n",
	})
	c.collectFS = func() ([]*hostmetricpb.FilesystemMetric, error) {
		return nil, errors.New("mounts unavailable")
	}

	snapshot, statuses, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.NotNil(t, snapshot.GetCpu())
	assert.Equal(t, uint64(1024*1024), snapshot.GetMemory().GetTotalBytes())
	require.Len(t, statuses, 5)
	for _, collector := range []string{"disk", "network", "filesystem"} {
		status := findStatus(t, statuses, collector)
		assert.False(t, status.GetSuccess())
		assert.NotEmpty(t, status.GetError())
	}
}

func TestCollectorNetworkRatesHandleFirstSampleAndCounterWrap(t *testing.T) {
	now := time.Unix(100, 0)
	files := map[string]string{
		"/proc/stat":      "cpu 1 2 3 40 5 6 7 8\n",
		"/proc/meminfo":   "MemTotal: 1024 kB\nMemAvailable: 512 kB\n",
		"/proc/diskstats": "",
		"/proc/net/dev":   networkData(100, 4, 200, 8),
	}
	c := testLinuxCollector(files)
	c.now = func() time.Time { return now }

	first, _, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, first.GetNetworks(), 1)
	assert.False(t, first.GetNetworks()[0].GetRateAvailable())
	assert.False(t, first.GetNetworks()[0].GetErrorRateAvailable())

	now = now.Add(2 * time.Second)
	files["/proc/net/dev"] = networkData(300, 10, 500, 12)
	second, _, err := c.Collect(context.Background())
	require.NoError(t, err)
	network := second.GetNetworks()[0]
	assert.True(t, network.GetRateAvailable())
	assert.Equal(t, 100.0, network.GetReceiveBytesPerSecond())
	assert.Equal(t, 150.0, network.GetTransmitBytesPerSecond())
	assert.True(t, network.GetErrorRateAvailable())
	assert.Equal(t, 3.0, network.GetReceiveErrorsPerSecond())
	assert.Equal(t, 2.0, network.GetTransmitErrorsPerSecond())

	now = now.Add(time.Second)
	files["/proc/net/dev"] = networkData(1, 1, 2, 1)
	wrapped, _, err := c.Collect(context.Background())
	require.NoError(t, err)
	assert.False(t, wrapped.GetNetworks()[0].GetRateAvailable())
	assert.False(t, wrapped.GetNetworks()[0].GetErrorRateAvailable())
}

func TestCollectorNetworkRateUnavailableAfterDeviceDisappears(t *testing.T) {
	now := time.Unix(100, 0)
	files := map[string]string{
		"/proc/stat":      "cpu 1 2 3 40 5 6 7 8\n",
		"/proc/meminfo":   "MemTotal: 1024 kB\nMemAvailable: 512 kB\n",
		"/proc/diskstats": "",
		"/proc/net/dev":   networkData(100, 1, 100, 1),
	}
	c := testLinuxCollector(files)
	c.now = func() time.Time { return now }
	_, _, err := c.Collect(context.Background())
	require.NoError(t, err)

	now = now.Add(time.Second)
	files["/proc/net/dev"] = ""
	_, _, err = c.Collect(context.Background())
	require.NoError(t, err)

	now = now.Add(time.Second)
	files["/proc/net/dev"] = networkData(200, 2, 200, 2)
	reappeared, _, err := c.Collect(context.Background())
	require.NoError(t, err)
	assert.False(t, reappeared.GetNetworks()[0].GetRateAvailable())
	assert.False(t, reappeared.GetNetworks()[0].GetErrorRateAvailable())
}

func testLinuxCollector(files map[string]string) *Collector {
	c := New()
	c.readFile = func(path string) ([]byte, error) {
		value, ok := files[path]
		if !ok {
			return nil, errors.New("unexpected read " + path)
		}
		return []byte(value), nil
	}
	c.collectFS = func() ([]*hostmetricpb.FilesystemMetric, error) { return nil, nil }
	return c
}

func findStatus(t *testing.T, statuses []*hostmetricpb.CollectorStatus, name string) *hostmetricpb.CollectorStatus {
	t.Helper()
	for _, status := range statuses {
		if status.GetCollector() == name {
			return status
		}
	}
	t.Fatalf("collector status %q not found", name)
	return nil
}

func networkData(receiveBytes, receiveErrors, transmitBytes, transmitErrors uint64) string {
	return "Inter-| Receive | Transmit\n face | fields\neth0: " +
		fmt.Sprintf("%d 0 %d 0 0 0 0 0 %d 0 %d 0 0 0 0 0\n",
			receiveBytes, receiveErrors, transmitBytes, transmitErrors)
}
