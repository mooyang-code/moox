package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	"gopkg.in/yaml.v3"
	"gorm.io/gorm"
)

const maxServiceDeploymentSeedBytes = 4 << 20

type serviceDeploymentSeed struct {
	Version  int                      `yaml:"version"`
	Node     serviceDeploymentNode    `yaml:"node"`
	Services []serviceDeploymentEntry `yaml:"services"`
}

type serviceDeploymentNode struct {
	ID            string `yaml:"id"`
	Name          string `yaml:"name"`
	PublicAddress string `yaml:"public_address"`
	Status        string `yaml:"status"`
}

type serviceDeploymentEntry struct {
	Name           string         `yaml:"name"`
	Kind           string         `yaml:"kind"`
	Protocol       string         `yaml:"protocol"`
	Host           string         `yaml:"host"`
	Port           int32          `yaml:"port"`
	GatewayPath    string         `yaml:"gateway_path"`
	GatewayService string         `yaml:"gateway_service_id"`
	GatewayEnabled bool           `yaml:"gateway_enabled"`
	Scope          string         `yaml:"scope"`
	Status         string         `yaml:"status"`
	Description    string         `yaml:"description"`
	DeploymentMode string         `yaml:"deployment_mode"`
	ExtraConfig    map[string]any `yaml:"extra_config"`
}

func isServiceDeploymentsCommand(args []string) bool {
	return len(args) > 1 && args[1] == "service-deployments"
}

func runServiceDeploymentsCommand(args []string, stdout io.Writer, stderr io.Writer) error {
	if len(args) < 2 || args[0] != "service-deployments" {
		return errors.New("expected service-deployments import subcommand")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if args[1] != "import" {
		return fmt.Errorf("unknown service-deployments subcommand %q", args[1])
	}

	fs := flag.NewFlagSet("service-deployments import", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath, seedPath := defaultInitDBPath, ""
	fs.StringVar(&dbPath, "db-path", dbPath, "SQLite database path")
	fs.StringVar(&seedPath, "file", seedPath, "service deployment seed YAML")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected service-deployments arguments: %s", strings.Join(fs.Args(), " "))
	}
	if strings.TrimSpace(seedPath) == "" {
		return errors.New("--file is required")
	}
	seed, err := loadServiceDeploymentSeed(seedPath)
	if err != nil {
		return err
	}
	if err := ensureAdminSchema(dbPath); err != nil {
		return err
	}
	db, err := openAdminCLIDB(dbPath)
	if err != nil {
		return err
	}
	defer closeAdminCLIDB(db)
	created, updated, err := importServiceDeploymentSeed(db, seed)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(map[string]any{
		"status": "ok", "command": "service-deployments.import", "node_id": seed.Node.ID,
		"created": created, "updated": updated, "services": len(seed.Services),
	})
}

func loadServiceDeploymentSeed(path string) (serviceDeploymentSeed, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return serviceDeploymentSeed{}, fmt.Errorf("read service deployment seed: %w", err)
	}
	if len(raw) > maxServiceDeploymentSeedBytes {
		return serviceDeploymentSeed{}, fmt.Errorf("service deployment seed exceeds %d bytes", maxServiceDeploymentSeedBytes)
	}
	var seed serviceDeploymentSeed
	if err := yaml.Unmarshal(raw, &seed); err != nil {
		return serviceDeploymentSeed{}, fmt.Errorf("parse service deployment seed: %w", err)
	}
	if err := validateServiceDeploymentSeed(seed); err != nil {
		return serviceDeploymentSeed{}, err
	}
	return seed, nil
}

func validateServiceDeploymentSeed(seed serviceDeploymentSeed) error {
	if seed.Version != 1 {
		return fmt.Errorf("service deployment seed version must be 1")
	}
	if strings.TrimSpace(seed.Node.ID) == "" || strings.TrimSpace(seed.Node.Name) == "" {
		return errors.New("service deployment seed node id and name are required")
	}
	if strings.TrimSpace(seed.Node.PublicAddress) == "" {
		return errors.New("service deployment seed node public_address is required")
	}
	if seed.Node.Status != "enabled" && seed.Node.Status != "disabled" {
		return fmt.Errorf("invalid node status %q", seed.Node.Status)
	}
	if len(seed.Services) == 0 {
		return errors.New("service deployment seed must contain at least one service")
	}
	seen := make(map[string]struct{}, len(seed.Services))
	for _, item := range seed.Services {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			return errors.New("service deployment name is required")
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("duplicate service deployment %q", name)
		}
		seen[name] = struct{}{}
		if item.Kind == "" || item.Host == "" || item.Port < 1 || item.Port > 65535 {
			return fmt.Errorf("invalid service deployment %q", name)
		}
		if item.Protocol != "http" && item.Protocol != "https" && item.Protocol != "trpc" {
			return fmt.Errorf("invalid protocol for %q", name)
		}
		if item.Scope != "public" && item.Scope != "internal" {
			return fmt.Errorf("invalid scope for %q", name)
		}
		if item.Status == "" || (item.Status != "active" && item.Status != "disabled") {
			return fmt.Errorf("invalid status for %q", name)
		}
		if item.DeploymentMode != "process" && item.DeploymentMode != "endpoint" {
			return fmt.Errorf("invalid deployment_mode for %q", name)
		}
		if item.GatewayEnabled && strings.TrimSpace(item.GatewayService) == "" {
			return fmt.Errorf("gateway_service_id is required for %q", name)
		}
	}
	return nil
}

func importServiceDeploymentSeed(db *gorm.DB, seed serviceDeploymentSeed) (created, updated int, err error) {
	err = db.Transaction(func(tx *gorm.DB) error {
		var node sysdeploy.GatewayNode
		nodeResult := tx.Where("c_node_id = ?", seed.Node.ID).Find(&node)
		if nodeResult.Error != nil {
			return fmt.Errorf("find gateway node: %w", nodeResult.Error)
		}
		if nodeResult.RowsAffected > 0 {
			if err := tx.Model(&sysdeploy.GatewayNode{}).Where("c_node_id = ?", seed.Node.ID).Updates(map[string]any{
				"c_name": seed.Node.Name, "c_public_address": seed.Node.PublicAddress, "c_status": seed.Node.Status,
			}).Error; err != nil {
				return fmt.Errorf("update gateway node: %w", err)
			}
		} else {
			if err := tx.Create(&sysdeploy.GatewayNode{NodeID: seed.Node.ID, Name: seed.Node.Name, PublicAddress: seed.Node.PublicAddress, Status: seed.Node.Status}).Error; err != nil {
				return fmt.Errorf("create gateway node: %w", err)
			}
		}

		for _, item := range seed.Services {
			extraConfig := item.ExtraConfig
			if extraConfig == nil {
				extraConfig = map[string]any{}
			}
			extra, marshalErr := json.Marshal(extraConfig)
			if marshalErr != nil {
				return fmt.Errorf("encode extra_config for %q: %w", item.Name, marshalErr)
			}
			deployment := &sysdeploy.Deployment{
				NodeID: seed.Node.ID, ServiceName: item.Name, ServiceKind: item.Kind, Protocol: item.Protocol,
				Host: item.Host, Port: item.Port, GatewayPath: item.GatewayPath, GatewayServiceID: item.GatewayService,
				GatewayEnabled: item.GatewayEnabled, Scope: item.Scope, Status: item.Status, Description: item.Description,
				ExtraConfig: string(extra),
			}
			var existing sysdeploy.Deployment
			findResult := tx.Where("c_node_id = ? AND c_service_name = ?", seed.Node.ID, item.Name).Find(&existing)
			if findResult.Error != nil {
				return fmt.Errorf("find service deployment %q: %w", item.Name, findResult.Error)
			}
			if findResult.RowsAffected > 0 {
				if err := tx.Model(&existing).Updates(map[string]any{
					"c_service_kind": deployment.ServiceKind, "c_protocol": deployment.Protocol, "c_host": deployment.Host,
					"c_port": deployment.Port, "c_gateway_path": deployment.GatewayPath, "c_gateway_service_id": deployment.GatewayServiceID,
					"c_gateway_enabled": deployment.GatewayEnabled, "c_scope": deployment.Scope, "c_status": deployment.Status,
					"c_description": deployment.Description, "c_extra_config": deployment.ExtraConfig,
				}).Error; err != nil {
					return fmt.Errorf("update service deployment %q: %w", item.Name, err)
				}
				updated++
			} else {
				if err := tx.Create(deployment).Error; err != nil {
					return fmt.Errorf("create service deployment %q: %w", item.Name, err)
				}
				created++
			}
		}
		return nil
	})
	return created, updated, err
}
