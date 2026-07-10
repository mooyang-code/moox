package collector

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
)

type CPUStat struct{ User, Nice, System, Idle, IOWait, IRQ, SoftIRQ, Steal uint64 }

func ParseCPUStat(data []byte) (CPUStat, error) {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "cpu" {
			continue
		}
		values := make([]uint64, 8)
		for i := range values {
			if i+1 >= len(fields) {
				break
			}
			n, err := strconv.ParseUint(fields[i+1], 10, 64)
			if err != nil {
				return CPUStat{}, fmt.Errorf("parse cpu counter %q: %w", fields[i+1], err)
			}
			values[i] = n
		}
		return CPUStat{values[0], values[1], values[2], values[3], values[4], values[5], values[6], values[7]}, nil
	}
	return CPUStat{}, fmt.Errorf("aggregate cpu line not found")
}

func ParseMeminfo(data []byte) (total, available uint64, err error) {
	values := map[string]uint64{}
	s := bufio.NewScanner(strings.NewReader(string(data)))
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) < 2 {
			continue
		}
		n, parseErr := strconv.ParseUint(fields[1], 10, 64)
		if parseErr != nil {
			return 0, 0, fmt.Errorf("parse meminfo %s: %w", fields[0], parseErr)
		}
		if len(fields) > 2 && fields[2] == "kB" {
			n *= 1024
		}
		values[strings.TrimSuffix(fields[0], ":")] = n
	}
	if err := s.Err(); err != nil {
		return 0, 0, err
	}
	total = values["MemTotal"]
	available = values["MemAvailable"]
	if available == 0 {
		base := values["MemFree"] + values["Buffers"] + values["Cached"] + values["SReclaimable"]
		if base > values["Shmem"] {
			available = base - values["Shmem"]
		}
	}
	if total == 0 {
		return 0, 0, fmt.Errorf("MemTotal is missing")
	}
	if available > total {
		available = total
	}
	return total, available, nil
}

type DiskStat struct {
	Name                                                   string
	ReadOps, ReadSectors, WriteOps, WriteSectors, IOTimeMS uint64
}

func ParseDiskStats(data []byte) ([]DiskStat, error) {
	var out []DiskStat
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 14 {
			continue
		}
		values := make([]uint64, 11)
		for i := range values {
			n, err := strconv.ParseUint(fields[i+3], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("parse disk %s: %w", fields[2], err)
			}
			values[i] = n
		}
		out = append(out, DiskStat{Name: fields[2], ReadOps: values[0], ReadSectors: values[2], WriteOps: values[4], WriteSectors: values[6], IOTimeMS: values[9]})
	}
	return out, nil
}

type NetworkStat struct {
	Name                                                                                                                         string
	ReceiveBytes, ReceivePackets, ReceiveErrors, ReceiveDropped, TransmitBytes, TransmitPackets, TransmitErrors, TransmitDropped uint64
}

func ParseNetworkStats(data []byte) ([]NetworkStat, error) {
	var out []NetworkStat
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		fields := strings.Fields(parts[1])
		if name == "" || len(fields) < 16 {
			continue
		}
		values := make([]uint64, 16)
		for i := range values {
			n, err := strconv.ParseUint(fields[i], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("parse network %s: %w", name, err)
			}
			values[i] = n
		}
		out = append(out, NetworkStat{Name: name, ReceiveBytes: values[0], ReceivePackets: values[1], ReceiveErrors: values[2], ReceiveDropped: values[3], TransmitBytes: values[8], TransmitPackets: values[9], TransmitErrors: values[10], TransmitDropped: values[11]})
	}
	return out, nil
}
