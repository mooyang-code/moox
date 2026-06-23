//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
)

// 计时器服务需要各自独立的端口，不能复用业务服务端口。
const (
	portViewTimer    = 28301
	portArchiveTimer = 28302
)

// writeConfig 在工作目录写入一份隔离的 trpc_go.yaml 与 storage.yaml：
//   - 独立端口段（28xxx/29xxx），不与默认部署冲突；
//   - storage 各设备目录全部落在工作目录内，便于测试结束清理；
//   - view.timer 每 5s 触发一次，archive.timer 每 20s 触发一次，
//     让视图物化与归档在 e2e 中能较快完成；
//   - 显式启用 access/primary/deriver，使用异步内存 eventbus 和本地 PrimaryStore。
func (h *Harness) writeConfig() error {
	storageRoot := filepath.Join(h.workDir, "var", "storage")
	if err := os.MkdirAll(storageRoot, 0o755); err != nil {
		return fmt.Errorf("创建 storage root 失败: %w", err)
	}
	// 参数顺序必须与模板中占位符出现的文本顺序一致。
	storageContent := fmt.Sprintf(storageConfigTemplate,
		storageRoot, // storage.root
		filepath.Join(storageRoot, "metadata", "metadata.db"), // metadata.path
		filepath.Join(storageRoot, "pebble"),                  // pebble_path
		filepath.Join(storageRoot, "duckdb", "views.duckdb"),  // duckdb_path
		filepath.Join(storageRoot, "bleve"),                   // bleve_path
		filepath.Join(storageRoot, "archive"),                 // parquet_path
	)
	if err := os.WriteFile(h.storageCfg, []byte(storageContent), 0o644); err != nil {
		return fmt.Errorf("写入 e2e storage 配置失败: %w", err)
	}
	trpcContent := fmt.Sprintf(configTemplate,
		portAdmin,        // admin.port
		portQuery,        // ViewService
		portPrimary,      // PrimaryStoreService
		portViewTimer,    // view.timer
		portArchiveTimer, // archive.timer
		e2eSpaceID,       // archive timer space_id
		portDataHTTP,     // AccessService HTTP
		portMetadataHTTP, // MetadataService HTTP
	)
	if err := os.WriteFile(h.configPth, []byte(trpcContent), 0o644); err != nil {
		return fmt.Errorf("写入 e2e 配置失败: %w", err)
	}
	return nil
}

const storageConfigTemplate = `
storage:
  root: %s
  roles:
    - access
    - primary
    - deriver
  metadata:
    path: %s
  devices:
    pebble_path: %s
    duckdb_path: %s
    bleve_path: %s
    parquet_path: %s
  primary:
    service_name: ""
  eventbus:
    type: memory
    stream_name: MOOX_STORAGE_E2E
  deriver:
    access_service_name: ""
    batch_size: 100
    batch_wait_ms: 50
    max_workers: 1
`

const configTemplate = `global:
  namespace: Development
  env_name: e2e

server:
  timeout: 5000
  admin:
    ip: 127.0.0.1
    port: %d
    read_timeout: 5000
    write_timeout: 60000
  service:
    - name: trpc.storage.view.ViewService
      ip: 127.0.0.1
      port: %d
      network: tcp
      protocol: trpc
    - name: trpc.storage.store.PrimaryStoreService
      ip: 127.0.0.1
      port: %d
      network: tcp
      protocol: trpc
    - name: trpc.storage.view.timer
      port: %d
      network: "*/5 * * * * *?scheduler=viewBuilderSchedule&startAtOnce=1&params="
      protocol: timer
      timeout: 60000
    - name: trpc.storage.archive.timer
      port: %d
      network: "*/20 * * * * *?scheduler=archiveSchedule&params=space_id=%s;dataset_id=*"
      protocol: timer
      timeout: 60000
    - name: trpc.storage.access.AccessService
      ip: 127.0.0.1
      port: %d
      network: tcp
      protocol: http
    - name: trpc.storage.metadata.MetadataService
      ip: 127.0.0.1
      port: %d
      network: tcp
      protocol: http

plugins:
  log:
    default:
      - writer: console
        level: info
`
