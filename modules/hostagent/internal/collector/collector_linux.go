//go:build linux

package collector

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"golang.org/x/sys/unix"
	trpc "trpc.group/trpc-go/trpc-go"
)

type Collector struct {
	mu         sync.Mutex
	readFile   func(string) ([]byte, error)
	collectFS  func() ([]*hostmetricpb.FilesystemMetric, error)
	now        func() time.Time
	prevCPU    CPUStat
	prevCPUOK  bool
	prevDiskAt time.Time
	prevDisk   map[string]DiskStat
	prevNetAt  time.Time
	prevNet    map[string]NetworkStat
}

func New() *Collector {
	return &Collector{
		readFile: os.ReadFile, collectFS: filesystemMetrics, now: time.Now,
		prevDisk: map[string]DiskStat{}, prevNet: map[string]NetworkStat{},
	}
}

func (c *Collector) Collect(ctx context.Context) (*hostmetricpb.HostSnapshot, []*hostmetricpb.CollectorStatus, error) {
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	statuses := make([]*hostmetricpb.CollectorStatus, 0, 5)
	read := func(name, path string) ([]byte, error) {
		b, err := c.readFile(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		return b, nil
	}

	snap := &hostmetricpb.HostSnapshot{Cpu: &hostmetricpb.CpuMetric{LogicalCores: uint32(runtime.NumCPU())}, Memory: &hostmetricpb.MemoryMetric{}}
	cpuData, cpuErr := read("cpu", "/proc/stat")
	var currentCPU CPUStat
	if cpuErr == nil {
		currentCPU, cpuErr = ParseCPUStat(cpuData)
	}
	statuses = append(statuses, status("cpu", cpuErr))

	memData, memErr := read("memory", "/proc/meminfo")
	var totalMemory, availableMemory uint64
	if memErr == nil {
		totalMemory, availableMemory, memErr = ParseMeminfo(memData)
	}
	statuses = append(statuses, status("memory", memErr))

	if cpuErr != nil || memErr != nil {
		return nil, statuses, fmt.Errorf("required host collectors failed: %w", errors.Join(cpuErr, memErr))
	}
	totalCPU := cpuTotal(currentCPU)
	busyCPU := cpuBusy(currentCPU)
	if c.prevCPUOK {
		previousTotal := cpuTotal(c.prevCPU)
		previousBusy := cpuBusy(c.prevCPU)
		if cpuMonotonic(currentCPU, c.prevCPU) && totalCPU >= previousTotal && busyCPU >= previousBusy && totalCPU > previousTotal {
			snap.Cpu.UsagePercent = float64(busyCPU-previousBusy) * 100 / float64(totalCPU-previousTotal)
			snap.Cpu.UsageAvailable = true
		}
	}
	c.prevCPU, c.prevCPUOK = currentCPU, true
	snap.Memory.TotalBytes, snap.Memory.AvailableBytes = totalMemory, availableMemory
	snap.Memory.UsedBytes = totalMemory - availableMemory
	snap.Memory.UsagePercent = float64(snap.Memory.UsedBytes) * 100 / float64(totalMemory)

	diskData, diskErr := read("disk", "/proc/diskstats")
	if diskErr == nil {
		stats, err := ParseDiskStats(diskData)
		if err == nil {
			for _, d := range stats {
				if isPartition(d.Name) {
					continue
				}
				metric := &hostmetricpb.DiskMetric{Device: d.Name, ReadBytesTotal: d.ReadSectors * 512, WriteBytesTotal: d.WriteSectors * 512, ReadOpsTotal: d.ReadOps, WriteOpsTotal: d.WriteOps, IoTimeMsTotal: d.IOTimeMS}
				if previous, ok := c.prevDisk[d.Name]; ok {
					elapsed := now.Sub(c.prevDiskAt).Seconds()
					if elapsed > 0 &&
						d.ReadSectors >= previous.ReadSectors && d.WriteSectors >= previous.WriteSectors &&
						d.ReadOps >= previous.ReadOps && d.WriteOps >= previous.WriteOps &&
						d.IOTimeMS >= previous.IOTimeMS {
						metric.ReadBytesPerSecond = float64(d.ReadSectors-previous.ReadSectors) * 512 / elapsed
						metric.WriteBytesPerSecond = float64(d.WriteSectors-previous.WriteSectors) * 512 / elapsed
						metric.ReadIops = float64(d.ReadOps-previous.ReadOps) / elapsed
						metric.WriteIops = float64(d.WriteOps-previous.WriteOps) / elapsed
						metric.UtilizationPercent = float64(d.IOTimeMS-previous.IOTimeMS) / (elapsed * 10)
						if metric.UtilizationPercent > 100 {
							metric.UtilizationPercent = 100
						}
						metric.RateAvailable = true
					}
				}
				snap.Disks = append(snap.Disks, metric)
			}
			c.prevDisk = make(map[string]DiskStat, len(stats))
			for _, d := range stats {
				c.prevDisk[d.Name] = d
			}
			c.prevDiskAt = now
			statuses = append(statuses, status("disk", nil))
		} else {
			statuses = append(statuses, status("disk", err))
		}
	} else {
		statuses = append(statuses, status("disk", diskErr))
	}

	netData, netErr := read("network", "/proc/net/dev")
	if netErr == nil {
		stats, err := ParseNetworkStats(netData)
		if err == nil {
			for _, n := range stats {
				metric := &hostmetricpb.NetworkMetric{Device: n.Name, ReceiveBytesTotal: n.ReceiveBytes, TransmitBytesTotal: n.TransmitBytes, ReceiveErrorsTotal: n.ReceiveErrors, TransmitErrorsTotal: n.TransmitErrors, ReceiveDroppedTotal: n.ReceiveDropped, TransmitDroppedTotal: n.TransmitDropped, Operstate: readOperstate(n.Name)}
				if previous, ok := c.prevNet[n.Name]; ok {
					elapsed := now.Sub(c.prevNetAt).Seconds()
					if elapsed > 0 && n.ReceiveBytes >= previous.ReceiveBytes && n.TransmitBytes >= previous.TransmitBytes {
						metric.ReceiveBytesPerSecond = float64(n.ReceiveBytes-previous.ReceiveBytes) / elapsed
						metric.TransmitBytesPerSecond = float64(n.TransmitBytes-previous.TransmitBytes) / elapsed
						metric.RateAvailable = true
					}
					if elapsed > 0 && n.ReceiveErrors >= previous.ReceiveErrors && n.TransmitErrors >= previous.TransmitErrors {
						metric.ReceiveErrorsPerSecond = float64(n.ReceiveErrors-previous.ReceiveErrors) / elapsed
						metric.TransmitErrorsPerSecond = float64(n.TransmitErrors-previous.TransmitErrors) / elapsed
						metric.ErrorRateAvailable = true
					}
				}
				snap.Networks = append(snap.Networks, metric)
			}
			c.prevNet = make(map[string]NetworkStat, len(stats))
			for _, n := range stats {
				c.prevNet[n.Name] = n
			}
			c.prevNetAt = now
			statuses = append(statuses, status("network", nil))
		} else {
			statuses = append(statuses, status("network", err))
		}
	} else {
		statuses = append(statuses, status("network", netErr))
	}

	fs, fsErr := c.collectFS()
	if fsErr != nil {
		statuses = append(statuses, status("filesystem", fsErr))
	} else {
		snap.Filesystems = fs
		statuses = append(statuses, status("filesystem", nil))
	}
	sort.Slice(snap.Disks, func(i, j int) bool { return snap.Disks[i].Device < snap.Disks[j].Device })
	sort.Slice(snap.Networks, func(i, j int) bool { return snap.Networks[i].Device < snap.Networks[j].Device })
	sort.Slice(snap.Filesystems, func(i, j int) bool { return snap.Filesystems[i].Mountpoint < snap.Filesystems[j].Mountpoint })
	snap.Collectors = statuses
	return snap, statuses, nil
}

func cpuTotal(stat CPUStat) uint64 {
	return stat.User + stat.Nice + stat.System + stat.Idle + stat.IOWait + stat.IRQ + stat.SoftIRQ + stat.Steal
}

func cpuBusy(stat CPUStat) uint64 {
	return stat.User + stat.Nice + stat.System + stat.IRQ + stat.SoftIRQ + stat.Steal
}

func cpuMonotonic(current, previous CPUStat) bool {
	return current.User >= previous.User &&
		current.Nice >= previous.Nice &&
		current.System >= previous.System &&
		current.Idle >= previous.Idle &&
		current.IOWait >= previous.IOWait &&
		current.IRQ >= previous.IRQ &&
		current.SoftIRQ >= previous.SoftIRQ &&
		current.Steal >= previous.Steal
}

func status(name string, err error) *hostmetricpb.CollectorStatus {
	s := &hostmetricpb.CollectorStatus{Collector: name, Success: err == nil}
	if err != nil {
		s.Error = truncate(err.Error(), 512)
	}
	return s
}
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
func isPartition(name string) bool {
	if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") || strings.HasPrefix(name, "fd") || strings.HasPrefix(name, "sr") {
		return true
	}
	_, err := os.Stat(filepath.Join("/sys/class/block", name, "partition"))
	return err == nil
}
func readOperstate(name string) string {
	b, err := os.ReadFile(filepath.Join("/sys/class/net", name, "operstate"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func filesystemMetrics() ([]*hostmetricpb.FilesystemMetric, error) {
	b, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return nil, err
	}
	mounts, err := ParseMounts(b)
	if err != nil {
		return nil, err
	}
	var out []*hostmetricpb.FilesystemMetric
	for _, mount := range mounts {
		if excludedFS(mount.FSType) {
			continue
		}
		var st unix.Statfs_t
		if err := unix.Statfs(mount.Mountpoint, &st); err != nil {
			return nil, fmt.Errorf("statfs %q: %w", mount.Mountpoint, err)
		}
		blockSize := uint64(st.Bsize)
		total := st.Blocks * blockSize
		available := st.Bavail * blockSize
		used := uint64(0)
		if total >= available {
			used = total - available
		}
		metric := &hostmetricpb.FilesystemMetric{
			Device: mount.Device, Mountpoint: mount.Mountpoint, FsType: mount.FSType,
			TotalBytes: total, UsedBytes: used, AvailableBytes: available, ReadOnly: mount.ReadOnly,
		}
		if total > 0 {
			metric.UsagePercent = float64(used) * 100 / float64(total)
		}
		out = append(out, metric)
	}
	return out, nil
}
func excludedFS(fs string) bool {
	switch fs {
	case "proc", "sysfs", "devtmpfs", "devpts", "tmpfs", "cgroup", "cgroup2", "overlay", "squashfs", "pstore", "securityfs", "debugfs", "tracefs", "configfs", "fusectl", "mqueue", "hugetlbfs", "autofs", "binfmt_misc":
		return true
	}
	return strings.HasPrefix(fs, "nfs") || strings.HasPrefix(fs, "fuse")
}
