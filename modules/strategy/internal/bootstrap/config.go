package bootstrap

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type EventBusConfig struct {
	URLs              []string      `yaml:"urls"`
	CredentialFile    string        `yaml:"credential_file"`
	RelayInterval     time.Duration `yaml:"relay_interval"`
	RelayBatchSize    int           `yaml:"relay_batch_size"`
	ReconnectInterval time.Duration `yaml:"reconnect_interval"`
	ConnectTimeout    time.Duration `yaml:"connect_timeout"`
}

type Config struct {
	PythonBin            string         `yaml:"python_bin"`
	WorkerPath           string         `yaml:"worker_path"`
	Database             string         `yaml:"database"`
	LogicalAccountTarget string         `yaml:"logical_account_target"`
	Workers              int            `yaml:"workers"`
	InstanceID           string         `yaml:"instance_id"`
	EventBus             EventBusConfig `yaml:"eventbus"`
}

func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err = yaml.Unmarshal(b, &c); err != nil {
		return Config{}, err
	}
	if c.PythonBin == "" {
		c.PythonBin = "python3"
	}
	if c.Workers < 1 {
		c.Workers = 1
	}
	if strings.TrimSpace(c.InstanceID) == "" {
		c.InstanceID = "strategy-1"
	}
	if strings.TrimSpace(c.LogicalAccountTarget) == "" {
		c.LogicalAccountTarget = "ip://127.0.0.1:11200"
	}
	if len(c.EventBus.URLs) == 0 {
		c.EventBus.URLs = []string{"nats://127.0.0.1:4222"}
	}
	if c.EventBus.RelayInterval == 0 {
		c.EventBus.RelayInterval = time.Second
	}
	if c.EventBus.RelayBatchSize == 0 {
		c.EventBus.RelayBatchSize = 100
	}
	if c.EventBus.ReconnectInterval == 0 {
		c.EventBus.ReconnectInterval = time.Second
	}
	if c.EventBus.ConnectTimeout == 0 {
		c.EventBus.ConnectTimeout = 3 * time.Second
	}
	if c.WorkerPath == "" {
		return Config{}, fmt.Errorf("worker_path is required for strategy execution")
	}
	for _, rawURL := range c.EventBus.URLs {
		if strings.TrimSpace(rawURL) == "" {
			return Config{}, fmt.Errorf("strategy eventbus URLs must not be empty")
		}
	}
	if c.EventBus.RelayInterval <= 0 || c.EventBus.RelayBatchSize <= 0 || c.EventBus.ReconnectInterval <= 0 || c.EventBus.ConnectTimeout <= 0 {
		return Config{}, fmt.Errorf("strategy eventbus durations and batch size must be positive")
	}
	return c, nil
}
