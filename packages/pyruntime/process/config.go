package process

import (
	"github.com/mooyang-code/moox/packages/pyruntime/protocol"
	"time"
)

type Config struct {
	PythonBin   string
	WorkerPath  string
	Args        []string
	Limits      protocol.Limits
	Hello       protocol.HelloExpectation
	TaskTimeout time.Duration
	MaxLogBytes int
}

func (c *Config) defaults() {
	if c.PythonBin == "" {
		c.PythonBin = "python3"
	}
	if c.TaskTimeout <= 0 {
		c.TaskTimeout = 30 * time.Second
	}
	if c.MaxLogBytes <= 0 {
		c.MaxLogBytes = 64 << 10
	}
	if c.Limits.MaxFrameBytes == 0 {
		c.Limits = protocol.DefaultLimits()
	}
}

func DefaultLimits() protocol.Limits { return protocol.DefaultLimits() }
