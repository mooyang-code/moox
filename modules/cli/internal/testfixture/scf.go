package testfixture

import (
	"debug/elf"
	"encoding/binary"
)

// LinuxExecutable returns a minimal Linux ELF which exits successfully. Its
// executable load segment covers both headers and machine instructions.
func LinuxExecutable(machine elf.Machine) []byte {
	code := []byte{0xb8, 0x3c, 0, 0, 0, 0xbf, 0, 0, 0, 0, 0x0f, 0x05}
	if machine == elf.EM_AARCH64 {
		code = []byte{0xa8, 0x0b, 0x80, 0xd2, 0, 0, 0x80, 0xd2, 1, 0, 0, 0xd4}
	}
	data := make([]byte, 120+len(code))
	copy(data, []byte{0x7f, 'E', 'L', 'F', byte(elf.ELFCLASS64), byte(elf.ELFDATA2LSB), byte(elf.EV_CURRENT)})
	put16 := func(offset int, value uint16) { binary.LittleEndian.PutUint16(data[offset:], value) }
	put32 := func(offset int, value uint32) { binary.LittleEndian.PutUint32(data[offset:], value) }
	put64 := func(offset int, value uint64) { binary.LittleEndian.PutUint64(data[offset:], value) }
	put16(16, uint16(elf.ET_EXEC))
	put16(18, uint16(machine))
	put32(20, uint32(elf.EV_CURRENT))
	put64(24, 0x400000+120)
	put64(32, 64)
	put16(52, 64)
	put16(54, 56)
	put16(56, 1)
	put32(64, uint32(elf.PT_LOAD))
	put32(68, uint32(elf.PF_R|elf.PF_X))
	put64(80, 0x400000)
	put64(88, 0x400000)
	put64(96, uint64(len(data)))
	put64(104, uint64(len(data)))
	put64(112, 0x1000)
	copy(data[120:], code)
	return data
}
