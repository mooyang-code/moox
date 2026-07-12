package collector

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsers(t *testing.T) {
	cpu, err := ParseCPUStat([]byte("cpu  10 2 3 4 1 0 0 0\n"))
	if err != nil || cpu.User != 10 || cpu.Idle != 4 {
		t.Fatalf("cpu=%+v err=%v", cpu, err)
	}
	total, available, err := ParseMeminfo([]byte("MemTotal: 100 kB\nMemFree: 20 kB\nBuffers: 10 kB\nCached: 30 kB\nShmem: 2 kB\n"))
	if err != nil || total != 102400 || available != 59392 {
		t.Fatalf("memory total=%d available=%d err=%v", total, available, err)
	}
	disks, err := ParseDiskStats([]byte("8 0 sda 1 0 2 0 3 0 4 0 5 6 0 0"))
	if err != nil || len(disks) != 1 || disks[0].Name != "sda" || disks[0].ReadSectors != 2 {
		t.Fatalf("disks=%+v err=%v", disks, err)
	}
}

func TestParseNetworkStats_ValidLine_ShouldParseDevice(t *testing.T) {
	data := []byte(`Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
  eth0: 1000 10 1 2 0 0 0 0 2000 20 3 4 0 0 0 0
`)
	stats, err := ParseNetworkStats(data)
	require.NoError(t, err)
	require.Len(t, stats, 1)
	assert.Equal(t, "eth0", stats[0].Name)
	assert.Equal(t, uint64(1000), stats[0].ReceiveBytes)
	assert.Equal(t, uint64(2000), stats[0].TransmitBytes)
}

func TestParseCPUStat_MissingAggregateLine_ShouldReturnError(t *testing.T) {
	_, err := ParseCPUStat([]byte("cpu0 1 2 3 4\n"))
	assert.Error(t, err)
}
