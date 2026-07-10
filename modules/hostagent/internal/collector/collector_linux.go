//go:build linux

package collector

import (
	"context"
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
)

type Collector struct {
	mu        sync.Mutex
	prevAt    time.Time
	prevCPU   CPUStat
	prevCPUOK bool
	prevDisk  map[string]DiskStat
	prevNet   map[string]NetworkStat
}

func New() *Collector {
	return &Collector{prevDisk: map[string]DiskStat{}, prevNet: map[string]NetworkStat{}}
}

func (c *Collector) Collect(ctx context.Context) (*hostmetricpb.HostSnapshot, []*hostmetricpb.CollectorStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	statuses := make([]*hostmetricpb.CollectorStatus, 0, 5)
	read := func(name, path string) ([]byte, error) {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		return b, nil
	}

	snap := &hostmetricpb.HostSnapshot{Cpu: &hostmetricpb.CpuMetric{LogicalCores: uint32(runtime.NumCPU())}, Memory: &hostmetricpb.MemoryMetric{}}
	cpuData, cpuErr := read("cpu", "/proc/stat")
	if cpuErr == nil {
		stat, err := ParseCPUStat(cpuData)
		if err == nil {
			total := stat.User + stat.Nice + stat.System + stat.Idle + stat.IOWait + stat.IRQ + stat.SoftIRQ + stat.Steal
			if c.prevCPUOK {
				previous := c.prevCPU.User + c.prevCPU.Nice + c.prevCPU.System + c.prevCPU.Idle + c.prevCPU.IOWait + c.prevCPU.IRQ + c.prevCPU.SoftIRQ + c.prevCPU.Steal
				busyTotal := stat.User + stat.Nice + stat.System + stat.IRQ + stat.SoftIRQ + stat.Steal
				previousBusy := c.prevCPU.User + c.prevCPU.Nice + c.prevCPU.System + c.prevCPU.IRQ + c.prevCPU.SoftIRQ + c.prevCPU.Steal
				if total >= previous && busyTotal >= previousBusy && total-previous > 0 {
					delta := total - previous
					snap.Cpu.UsagePercent = float64(busyTotal-previousBusy) * 100 / float64(delta)
					snap.Cpu.UsageAvailable = true
				}
			}
			c.prevCPU, c.prevCPUOK = stat, true
			statuses = append(statuses, status("cpu", nil))
		} else {
			statuses = append(statuses, status("cpu", err))
		}
	} else {
		statuses = append(statuses, status("cpu", cpuErr))
	}

	memData, memErr := read("memory", "/proc/meminfo")
	if memErr == nil {
		total, available, err := ParseMeminfo(memData)
		if err == nil {
			snap.Memory.TotalBytes, snap.Memory.AvailableBytes = total, available
			snap.Memory.UsedBytes = total - available
			snap.Memory.UsagePercent = float64(snap.Memory.UsedBytes) * 100 / float64(total)
			statuses = append(statuses, status("memory", nil))
		} else {
			statuses = append(statuses, status("memory", err))
		}
	} else {
		statuses = append(statuses, status("memory", memErr))
	}

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
					elapsed := now.Sub(c.prevAt).Seconds()
					if elapsed > 0 && d.ReadSectors >= previous.ReadSectors && d.WriteSectors >= previous.WriteSectors && d.IOTimeMS >= previous.IOTimeMS {
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
					elapsed := now.Sub(c.prevAt).Seconds()
					if elapsed > 0 && n.ReceiveBytes >= previous.ReceiveBytes && n.TransmitBytes >= previous.TransmitBytes {
						metric.ReceiveBytesPerSecond = float64(n.ReceiveBytes-previous.ReceiveBytes) / elapsed
						metric.TransmitBytesPerSecond = float64(n.TransmitBytes-previous.TransmitBytes) / elapsed
						metric.RateAvailable = true
					}
				}
				snap.Networks = append(snap.Networks, metric)
			}
			c.prevNet = make(map[string]NetworkStat, len(stats))
			for _, n := range stats {
				c.prevNet[n.Name] = n
			}
			statuses = append(statuses, status("network", nil))
		} else {
			statuses = append(statuses, status("network", err))
		}
	} else {
		statuses = append(statuses, status("network", netErr))
	}

	fs, fsErr := filesystemMetrics()
	if fsErr != nil {
		statuses = append(statuses, status("filesystem", fsErr))
	} else {
		snap.Filesystems = fs
		statuses = append(statuses, status("filesystem", nil))
	}
	c.prevAt = now
	sort.Slice(snap.Disks, func(i, j int) bool { return snap.Disks[i].Device < snap.Disks[j].Device })
	sort.Slice(snap.Networks, func(i, j int) bool { return snap.Networks[i].Device < snap.Networks[j].Device })
	sort.Slice(snap.Filesystems, func(i, j int) bool { return snap.Filesystems[i].Mountpoint < snap.Filesystems[j].Mountpoint })
	snap.Collectors = statuses
	return snap, statuses, nil
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
	if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") || strings.HasPrefix(name, "fd") || strings.HasPrefix(name, "sr") { return true }
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
	b, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	var out []*hostmetricpb.FilesystemMetric
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		sep := -1
		for i, f := range fields {
			if f == "-" {
				sep = i
				break
			}
		}
		if sep < 6 || sep+3 > len(fields) {
			continue
		}
		fsType := fields[sep+1]
		if excludedFS(fsType) {
			continue
		}
		mount := strings.ReplaceAll(strings.ReplaceAll(fields[4], "\\040", " "), "\\011", "\t")
		var st unix.Statfs_t
		if err := unix.Statfs(mount, &st); err != nil {
			continue
		}
		blockSize := uint64(st.Bsize)
		total := st.Blocks * blockSize
		available := st.Bavail * blockSize
		used := uint64(0)
		if total >= available {
			used = total - available
		}
		metric := &hostmetricpb.FilesystemMetric{Device: fields[sep+2], Mountpoint: mount, FsType: fsType, TotalBytes: total, UsedBytes: used, AvailableBytes: available, ReadOnly: strings.Contains(fields[5], "ro")}
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
