package control

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"os"
)

type Config struct {
	PythonBin   string `yaml:"python_bin"`
	WorkerPath  string `yaml:"worker_path"`
	Database    string `yaml:"database"`
	LiveEnabled bool   `yaml:"live_enabled"`
	Workers     int    `yaml:"workers"`
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
	if c.LiveEnabled && c.WorkerPath == "" {
		return Config{}, fmt.Errorf("worker_path is required when live is enabled")
	}
	return c, nil
}
