package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/jetstream"
	"gopkg.in/yaml.v3"
)

const defaultTradeAppConfigPath = "config/app.yaml"

type eventBusCheckAppConfig struct {
	EventBus struct {
		Enabled        bool     `yaml:"enabled"`
		URLs           []string `yaml:"urls"`
		CredentialFile string   `yaml:"credential_file"`
	} `yaml:"eventbus"`
}

type eventBusCheckResult struct {
	Module    string `json:"module"`
	Status    string `json:"status"`
	Connected bool   `json:"connected"`
	URL       string `json:"url,omitempty"`
}

func isEventBusCheckCommand(args []string) bool {
	return len(args) > 1 && args[1] == "eventbus-check"
}

func runEventBusCheckCommand(args []string, stdout io.Writer, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "eventbus-check" {
		return fmt.Errorf("expected eventbus-check command")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	fs := flag.NewFlagSet("eventbus-check", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := defaultTradeAppConfigPath
	credentialPath := ""
	fs.StringVar(&configPath, "config", configPath, "Trade app.yaml path")
	fs.StringVar(&credentialPath, "credential-file", credentialPath, "EventBus credential file path")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected eventbus-check arguments: %s", strings.Join(fs.Args(), " "))
	}

	appConfig, err := loadEventBusCheckAppConfig(configPath)
	if err != nil {
		return err
	}
	if !appConfig.EventBus.Enabled {
		return json.NewEncoder(stdout).Encode(eventBusCheckResult{
			Module: "trade", Status: "skipped", Connected: false,
		})
	}
	if value := strings.TrimSpace(os.Getenv("MOOX_EVENTBUS_CREDENTIAL_FILE")); value != "" {
		credentialPath = value
	}
	if strings.TrimSpace(credentialPath) == "" {
		credentialPath = appConfig.EventBus.CredentialFile
	}
	if strings.TrimSpace(credentialPath) == "" {
		credentialPath = "~/.config/moox/eventbus/trade-eventbus.yaml"
	}
	credentialPath = jetstream.ExpandCredentialPath(credentialPath)

	clientConfig := jetstream.ConfigFromEnv(appConfig.EventBus.URLs, "moox-trade-eventbus-check")
	if err := clientConfig.ApplyCredentialFile(credentialPath); err != nil {
		return fmt.Errorf("load EventBus credential: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := jetstream.Connect(ctx, clientConfig)
	if err != nil {
		return fmt.Errorf("connect EventBus: %w", err)
	}
	defer client.Close()

	url := ""
	if len(clientConfig.URLs) > 0 {
		url = clientConfig.URLs[0]
	}
	return json.NewEncoder(stdout).Encode(eventBusCheckResult{
		Module: "trade", Status: "ok", Connected: true, URL: url,
	})
}

func loadEventBusCheckAppConfig(path string) (eventBusCheckAppConfig, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return eventBusCheckAppConfig{}, fmt.Errorf("read app config: %w", err)
	}
	var config eventBusCheckAppConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return eventBusCheckAppConfig{}, fmt.Errorf("parse app config: %w", err)
	}
	return config, nil
}
