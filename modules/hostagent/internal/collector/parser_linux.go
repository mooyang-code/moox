//go:build linux

package collector

import (
	"bufio"
	"bytes"
	"fmt"
	"math"
	"strconv"
	"strings"
)

type CPUStat struct {
	User, Nice, System, Idle, IOWait, IRQ, SoftIRQ, Steal uint64
}

type DiskStat struct {
	Name                                         string
	ReadOps, ReadSectors, WriteOps, WriteSectors uint64
	IOTimeMS                                     uint64
}

type NetworkStat struct {
	Name                                           string
	ReceiveBytes, ReceiveErrors, ReceiveDropped    uint64
	TransmitBytes, TransmitErrors, TransmitDropped uint64
}

type Mount struct {
	Device, Mountpoint, FSType string
	ReadOnly                   bool
}

func ParseCPUStat(data []byte) (CPUStat, error) {
	scanner := newScanner(data)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		if fields[0] != "cpu" {
			return CPUStat{}, fmt.Errorf("/proc/stat: aggregate cpu line is missing")
		}
		if len(fields) < 9 {
			return CPUStat{}, fmt.Errorf("/proc/stat: aggregate cpu line has %d counters, want at least 8", len(fields)-1)
		}
		values := make([]uint64, 8)
		for i := range values {
			value, err := parseUint("/proc/stat", fields[i+1])
			if err != nil {
				return CPUStat{}, err
			}
			values[i] = value
		}
		return CPUStat{
			User: values[0], Nice: values[1], System: values[2], Idle: values[3],
			IOWait: values[4], IRQ: values[5], SoftIRQ: values[6], Steal: values[7],
		}, nil
	}
	if err := scanner.Err(); err != nil {
		return CPUStat{}, fmt.Errorf("/proc/stat: scan: %w", err)
	}
	return CPUStat{}, fmt.Errorf("/proc/stat: aggregate cpu line is missing")
}

func ParseMeminfo(data []byte) (uint64, uint64, error) {
	var totalKB, availableKB uint64
	var totalFound, availableFound bool
	scanner := newScanner(data)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "MemTotal:", "MemAvailable:":
			if len(fields) < 2 {
				return 0, 0, fmt.Errorf("/proc/meminfo: %s has no value", fields[0])
			}
			value, err := parseUint("/proc/meminfo", fields[1])
			if err != nil {
				return 0, 0, err
			}
			if len(fields) >= 3 && fields[2] != "kB" {
				return 0, 0, fmt.Errorf("/proc/meminfo: %s uses unsupported unit %q", fields[0], fields[2])
			}
			if value > math.MaxUint64/1024 {
				return 0, 0, fmt.Errorf("/proc/meminfo: %s overflows bytes", fields[0])
			}
			if fields[0] == "MemTotal:" {
				totalKB, totalFound = value, true
			} else {
				availableKB, availableFound = value, true
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, fmt.Errorf("/proc/meminfo: scan: %w", err)
	}
	if !totalFound || !availableFound {
		return 0, 0, fmt.Errorf("/proc/meminfo: MemTotal and MemAvailable are required")
	}
	if totalKB == 0 || availableKB > totalKB {
		return 0, 0, fmt.Errorf("/proc/meminfo: invalid total=%d kB available=%d kB", totalKB, availableKB)
	}
	return totalKB * 1024, availableKB * 1024, nil
}

func ParseDiskStats(data []byte) ([]DiskStat, error) {
	var result []DiskStat
	scanner := newScanner(data)
	line := 0
	for scanner.Scan() {
		line++
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 14 {
			return nil, fmt.Errorf("/proc/diskstats line %d: has %d fields, want at least 14", line, len(fields))
		}
		values := make([]uint64, 5)
		for i, index := range []int{3, 5, 7, 9, 12} {
			value, err := parseUint(fmt.Sprintf("/proc/diskstats line %d", line), fields[index])
			if err != nil {
				return nil, err
			}
			values[i] = value
		}
		result = append(result, DiskStat{
			Name: fields[2], ReadOps: values[0], ReadSectors: values[1],
			WriteOps: values[2], WriteSectors: values[3], IOTimeMS: values[4],
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("/proc/diskstats: scan: %w", err)
	}
	return result, nil
}

func ParseNetworkStats(data []byte) ([]NetworkStat, error) {
	var result []NetworkStat
	scanner := newScanner(data)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || !strings.Contains(text, ":") {
			continue
		}
		name, counters, ok := strings.Cut(text, ":")
		if !ok || strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("/proc/net/dev line %d: interface name is missing", line)
		}
		fields := strings.Fields(counters)
		if len(fields) < 16 {
			return nil, fmt.Errorf("/proc/net/dev line %d: has %d counters, want at least 16", line, len(fields))
		}
		values := make([]uint64, 6)
		for i, index := range []int{0, 2, 3, 8, 10, 11} {
			value, err := parseUint(fmt.Sprintf("/proc/net/dev line %d", line), fields[index])
			if err != nil {
				return nil, err
			}
			values[i] = value
		}
		result = append(result, NetworkStat{
			Name: strings.TrimSpace(name), ReceiveBytes: values[0],
			ReceiveErrors: values[1], ReceiveDropped: values[2],
			TransmitBytes: values[3], TransmitErrors: values[4], TransmitDropped: values[5],
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("/proc/net/dev: scan: %w", err)
	}
	return result, nil
}

func ParseMounts(data []byte) ([]Mount, error) {
	var result []Mount
	scanner := newScanner(data)
	line := 0
	for scanner.Scan() {
		line++
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 6 {
			return nil, fmt.Errorf("/proc/mounts line %d: has %d fields, want at least 6", line, len(fields))
		}
		options := strings.Split(fields[3], ",")
		readOnly := false
		for _, option := range options {
			if option == "ro" {
				readOnly = true
				break
			}
		}
		result = append(result, Mount{
			Device: decodeProcPath(fields[0]), Mountpoint: decodeProcPath(fields[1]),
			FSType: fields[2], ReadOnly: readOnly,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("/proc/mounts: scan: %w", err)
	}
	return result, nil
}

func newScanner(data []byte) *bufio.Scanner {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	return scanner
}

func parseUint(source, raw string) (uint64, error) {
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid counter %q: %w", source, raw, err)
	}
	return value, nil
}

func decodeProcPath(value string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(value)
}
