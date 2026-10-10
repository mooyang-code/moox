package deploy

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeMinimalELF 写一个只有文件头的 64 位小端 ELF，machine 是 e_machine。
func writeMinimalELF(t *testing.T, machine uint16) string {
	t.Helper()
	var header bytes.Buffer
	header.Write([]byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	for _, value := range []any{
		uint16(2), machine, uint32(1), // e_type、e_machine、e_version
		uint64(0), uint64(0), uint64(0), // e_entry、e_phoff、e_shoff
		uint32(0), uint16(64), uint16(56), uint16(0), uint16(64), uint16(0), uint16(0), // e_flags、ehsize、phentsize、phnum、shentsize、shnum、shstrndx
	} {
		require.NoError(t, binary.Write(&header, binary.LittleEndian, value))
	}
	path := filepath.Join(t.TempDir(), "moox-test")
	require.NoError(t, os.WriteFile(path, header.Bytes(), 0o755))
	return path
}

func TestVerifyLinuxBinaryChecksArchitecture(t *testing.T) {
	amd64 := writeMinimalELF(t, 62)
	arm64 := writeMinimalELF(t, 183)
	require.NoError(t, verifyLinuxBinary(amd64, "amd64"))
	require.NoError(t, verifyLinuxBinary(arm64, "arm64"))
	require.ErrorContains(t, verifyLinuxBinary(arm64, "amd64"), "架构")
	require.ErrorContains(t, verifyLinuxBinary(amd64, "arm64"), "架构")
	text := filepath.Join(t.TempDir(), "script")
	require.NoError(t, os.WriteFile(text, []byte("#!/bin/sh\n"), 0o755))
	require.ErrorContains(t, verifyLinuxBinary(text, "amd64"), "不是 Linux 可执行文件")
	require.ErrorContains(t, verifyLinuxBinary(amd64, "riscv64"), "不支持的目标架构")
}
