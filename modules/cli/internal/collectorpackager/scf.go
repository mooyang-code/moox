package collectorpackager

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"gopkg.in/yaml.v3"
)

// BuildSCFPackageOptions configures a Tencent SCF package build.
type BuildSCFPackageOptions struct {
	BinaryPath               string
	ConfigDir                string
	OutPath                  string
	CLSTopicID               string
	StoragePrimaryAuthSecret string
}

// BuildSCFPackageResult describes the created package.
type BuildSCFPackageResult struct {
	Path    string
	Entries []string
}

// BuildSCFPackage creates a zip containing main, config.yaml, trpc_go.yaml,
// and nested sources/ entries. When example_trpc_go.yaml is present, it is
// packaged as trpc_go.yaml so developer-local ignored configs do not leak into
// SCF packages.
func BuildSCFPackage(opts BuildSCFPackageOptions) (*BuildSCFPackageResult, error) {
	if opts.BinaryPath == "" {
		return nil, fmt.Errorf("binary path is required")
	}
	if opts.ConfigDir == "" {
		return nil, fmt.Errorf("config dir is required")
	}
	if opts.OutPath == "" {
		return nil, fmt.Errorf("output path is required")
	}
	if !validCLSTopicID(opts.CLSTopicID) {
		return nil, fmt.Errorf("CLS topic ID is required and must contain only letters, digits, dot, underscore, colon, or hyphen")
	}

	if err := os.MkdirAll(filepath.Dir(opts.OutPath), 0o755); err != nil {
		return nil, err
	}
	out, err := os.Create(opts.OutPath)
	if err != nil {
		return nil, err
	}
	defer out.Close()

	zw := zip.NewWriter(out)
	defer zw.Close()

	var entries []string
	addFile := func(src, dst string) error {
		if err := addZipFile(zw, src, dst); err != nil {
			return err
		}
		entries = append(entries, filepath.ToSlash(dst))
		return nil
	}

	if err := addFile(opts.BinaryPath, "main"); err != nil {
		return nil, err
	}

	configPath := filepath.Join(opts.ConfigDir, "config.yaml")
	if err := addFile(configPath, "config.yaml"); err != nil {
		return nil, err
	}

	trpcPath := filepath.Join(opts.ConfigDir, "example_trpc_go.yaml")
	if _, err := os.Stat(trpcPath); err == nil {
		if err := addRenderedTRPCConfig(zw, trpcPath, opts.CLSTopicID); err != nil {
			return nil, err
		}
		entries = append(entries, "trpc_go.yaml")
	} else if !os.IsNotExist(err) {
		return nil, err
	} else {
		trpcPath = filepath.Join(opts.ConfigDir, "trpc_go.yaml")
		if _, err := os.Stat(trpcPath); err == nil {
			if err := addRenderedTRPCConfig(zw, trpcPath, opts.CLSTopicID); err != nil {
				return nil, err
			}
			entries = append(entries, "trpc_go.yaml")
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}

	sourcesDir := filepath.Join(opts.ConfigDir, "sources")
	if _, err := os.Stat(sourcesDir); err == nil {
		err = filepath.WalkDir(sourcesDir, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(opts.ConfigDir, path)
			if err != nil {
				return err
			}
			if filepath.ToSlash(rel) == "sources/market/binance.yaml" {
				if err := addRenderedStorageAuthConfig(zw, path, rel, opts.StoragePrimaryAuthSecret); err != nil {
					return err
				}
				entries = append(entries, filepath.ToSlash(rel))
				return nil
			}
			return addFile(path, rel)
		})
		if err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	sort.Strings(entries)
	return &BuildSCFPackageResult{Path: opts.OutPath, Entries: entries}, nil
}

func addRenderedStorageAuthConfig(zw *zip.Writer, src, dst, secret string) error {
	if strings.TrimSpace(secret) == "" {
		return fmt.Errorf("MOOX_STORAGE_PRIMARY_AUTH_SECRET is required to package Binance Storage credentials")
	}
	content, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(content, &document); err != nil {
		return fmt.Errorf("parse Binance source config: %w", err)
	}
	rendered := 0
	var visit func(*yaml.Node) error
	visit = func(node *yaml.Node) error {
		if node.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(node.Content); i += 2 {
				key, value := node.Content[i], node.Content[i+1]
				if key.Value == "auth_info" && value.Kind == yaml.MappingNode {
					appID, appKey := mappingValue(value, "app_id"), mappingValue(value, "app_key")
					if appID == nil || strings.TrimSpace(appID.Value) == "" || appKey == nil {
						return fmt.Errorf("Binance Storage auth_info requires app_id and app_key")
					}
					appKey.Value = mooxsecurity.HMACSHA256Hex(secret, []byte(strings.TrimSpace(appID.Value)))
					rendered++
				}
				if err := visit(value); err != nil {
					return err
				}
			}
		} else {
			for _, child := range node.Content {
				if err := visit(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := visit(&document); err != nil {
		return err
	}
	if rendered == 0 {
		return fmt.Errorf("Binance source config contains no Storage auth_info")
	}
	var output strings.Builder
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		return fmt.Errorf("render Binance source config: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	return addZipBytes(zw, []byte(output.String()), dst, 0o644)
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

var clsTopicIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

func validCLSTopicID(topicID string) bool {
	return clsTopicIDPattern.MatchString(strings.TrimSpace(topicID))
}

// ValidateSCFPackageCLSTopic verifies that an existing package writes to the resolved topic.
func ValidateSCFPackageCLSTopic(packagePath, expectedTopicID string) error {
	if !validCLSTopicID(expectedTopicID) {
		return fmt.Errorf("expected CLS topic ID is invalid")
	}
	reader, err := zip.OpenReader(packagePath)
	if err != nil {
		return fmt.Errorf("open SCF package: %w", err)
	}
	defer reader.Close()
	for _, file := range reader.File {
		if file.Name != "trpc_go.yaml" {
			continue
		}
		stream, err := file.Open()
		if err != nil {
			return fmt.Errorf("open trpc_go.yaml in SCF package: %w", err)
		}
		content, readErr := io.ReadAll(stream)
		closeErr := stream.Close()
		if readErr != nil {
			return fmt.Errorf("read trpc_go.yaml in SCF package: %w", readErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close trpc_go.yaml in SCF package: %w", closeErr)
		}
		topicID, err := clsTopicIDFromTRPCConfig(content)
		if err != nil {
			return fmt.Errorf("validate trpc_go.yaml in SCF package: %w", err)
		}
		if topicID != strings.TrimSpace(expectedTopicID) {
			return fmt.Errorf("SCF package CLS topic %q does not match resolved topic %q; rebuild the package", topicID, expectedTopicID)
		}
		return nil
	}
	return fmt.Errorf("SCF package does not contain trpc_go.yaml")
}

func clsTopicIDFromTRPCConfig(source []byte) (string, error) {
	var document map[string]any
	if err := yaml.Unmarshal(source, &document); err != nil {
		return "", err
	}
	plugins, _ := document["plugins"].(map[string]any)
	logs, _ := plugins["log"].(map[string]any)
	writers, _ := logs["default"].([]any)
	var topicID string
	clsWriters := 0
	for _, writer := range writers {
		config, _ := writer.(map[string]any)
		if config["writer"] != "cls" {
			continue
		}
		clsWriters++
		remote, _ := config["remote_config"].(map[string]any)
		value, _ := remote["topic_id"].(string)
		if value = strings.TrimSpace(value); value == "" {
			return "", fmt.Errorf("CLS writer topic_id is missing")
		}
		topicID = value
	}
	if clsWriters != 1 {
		return "", fmt.Errorf("trpc_go.yaml must contain exactly one CLS writer, got %d", clsWriters)
	}
	return topicID, nil
}

func addRenderedTRPCConfig(zw *zip.Writer, sourcePath, topicID string) error {
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		return err
	}
	rendered, err := renderTRPCConfigWithCLS(source, strings.TrimSpace(topicID))
	if err != nil {
		return fmt.Errorf("render SCF trpc_go.yaml: %w", err)
	}
	header := &zip.FileHeader{Name: "trpc_go.yaml", Method: zip.Deflate}
	header.SetMode(0o644)
	w, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = w.Write(rendered)
	return err
}

func renderTRPCConfigWithCLS(source []byte, topicID string) ([]byte, error) {
	var document map[string]any
	if err := yaml.Unmarshal(source, &document); err != nil {
		return nil, err
	}
	plugins, _ := document["plugins"].(map[string]any)
	if plugins == nil {
		plugins = make(map[string]any)
		document["plugins"] = plugins
	}
	logs, _ := plugins["log"].(map[string]any)
	if logs == nil {
		logs = make(map[string]any)
		plugins["log"] = logs
	}
	writers, _ := logs["default"].([]any)
	filtered := make([]any, 0, len(writers)+1)
	for _, writer := range writers {
		config, ok := writer.(map[string]any)
		if ok && config["writer"] == "cls" {
			continue
		}
		filtered = append(filtered, writer)
	}
	filtered = append(filtered, map[string]any{
		"writer": "cls",
		"level":  "warn",
		"remote_config": map[string]any{
			"topic_id":      topicID,
			"host":          "${MOOX_CLS_HOST}",
			"secret_id":     "${MOOX_CLS_SECRET_ID}",
			"secret_key":    "${MOOX_CLS_SECRET_KEY}",
			"max_block_sec": 0,
		},
	})
	logs["default"] = filtered
	return yaml.Marshal(document)
}

func addZipFile(zw *zip.Writer, src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = filepath.ToSlash(dst)
	header.Method = zip.Deflate

	w, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	_, err = io.Copy(w, in)
	return err
}

func addZipBytes(zw *zip.Writer, content []byte, dst string, mode os.FileMode) error {
	header := &zip.FileHeader{Name: filepath.ToSlash(dst), Method: zip.Deflate}
	header.SetMode(mode)
	writer, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = writer.Write(content)
	return err
}
