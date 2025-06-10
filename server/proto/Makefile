# Makefile

# 定义路径变量
XDATA_PROTO_DIR := ../../../xData-mini/storage/proto
LOCAL_PROTO_DIR := ./
OUTPUT_DIR := ./gen

# 本地协议文件列表（在当前目录下的proto文件）
LOCAL_PROTO_FILES := \
	moox.proto

# 需要同步的协议文件列表（从xData-mini同步的协议文件）
SYNC_PROTO_FILES := \
	common.proto \
	metadata.proto

# 或者如果你想同步所有 .proto 文件，使用这个变量
# SYNC_ALL_PROTO := true

.PHONY: all
all: check-env sync-proto generate-local generate-synced
	@echo ""
	@echo "🎉 Build completed successfully!"
	@echo "Generated files are in: $(OUTPUT_DIR)"

# 同步协议文件
.PHONY: sync-proto
sync-proto:
	@echo "📥 Syncing proto files from xData-mini..."
	@if [ ! -d "$(XDATA_PROTO_DIR)" ]; then \
		echo "❌ Error: xData-mini proto directory not found at $(XDATA_PROTO_DIR)"; \
		exit 1; \
	fi
	@mkdir -p $(LOCAL_PROTO_DIR)
ifdef SYNC_ALL_PROTO
	@echo "Syncing ALL proto files..."
	@cp $(XDATA_PROTO_DIR)/*.proto $(LOCAL_PROTO_DIR)/ 2>/dev/null || true
	@echo "✅ All proto files synced"
else
	@echo "Syncing specified proto files..."
	@for file in $(SYNC_PROTO_FILES); do \
		if [ -f "$(XDATA_PROTO_DIR)/$$file" ]; then \
			cp "$(XDATA_PROTO_DIR)/$$file" "$(LOCAL_PROTO_DIR)/"; \
			echo "✅ Copied $$file"; \
		else \
			echo "⚠️  Warning: $$file not found in $(XDATA_PROTO_DIR)"; \
		fi \
	done
endif
	@echo "✅ Proto files synced successfully!"

# 生成本地 protobuf 代码
.PHONY: generate-local
generate-local:
	@echo "🔨 Generating protobuf code for local proto files..."
	@mkdir -p $(OUTPUT_DIR)
	@for file in $(LOCAL_PROTO_FILES); do \
		if [ -f "$$file" ]; then \
			echo "🔨 Processing local $$file..."; \
			trpc-open create \
				-p "$$file" \
				--rpconly \
				--nogomod \
				--mock=false \
				-o $(OUTPUT_DIR) || { \
				echo "❌ Error: trpc-open failed for $$file"; \
				exit 1; \
			}; \
			echo "✅ Generated code for local $$file"; \
		else \
			echo "⚠️  Warning: local $$file not found, skipping..."; \
		fi \
	done
	@echo "✅ Local protobuf code generated successfully!"

# 生成同步的 protobuf 代码  
.PHONY: generate-synced
generate-synced:
	@echo "🔨 Generating protobuf code for synced proto files..."
	@mkdir -p $(OUTPUT_DIR)
ifdef SYNC_ALL_PROTO
	@echo "Generating code for ALL synced proto files..."
	@for proto_file in *.proto; do \
		if [ -f "$$proto_file" ] && [ "$$proto_file" != "moox.proto" ]; then \
			echo "🔨 Processing synced $$proto_file..."; \
			trpc-open create \
				-p "$$proto_file" \
				--rpconly \
				--nogomod \
				--mock=false \
				-o $(OUTPUT_DIR) || { \
				echo "❌ Error: trpc-open failed for $$proto_file"; \
				exit 1; \
			}; \
			echo "✅ Generated code for synced $$proto_file"; \
		fi \
	done
else
	@echo "Generating code for configured synced proto files..."
	@for file in $(SYNC_PROTO_FILES); do \
		if [ -f "$$file" ]; then \
			echo "🔨 Processing synced $$file..."; \
			trpc-open create \
				-p "$$file" \
				--rpconly \
				--nogomod \
				--mock=false \
				-o $(OUTPUT_DIR) || { \
				echo "❌ Error: trpc-open failed for $$file"; \
				exit 1; \
			}; \
			echo "✅ Generated code for synced $$file"; \
		else \
			echo "⚠️  Warning: synced $$file not found locally, skipping..."; \
		fi \
	done
endif
	@echo "✅ Synced protobuf code generated successfully!"

# 生成所有 protobuf 代码（本地+同步）
.PHONY: generate
generate: generate-local generate-synced

# 为单个协议文件生成代码
.PHONY: generate-single
generate-single:
	@if [ -z "$(PROTO)" ]; then \
		echo "❌ Usage: make generate-single PROTO=filename.proto"; \
		exit 1; \
	fi
	@if [ -f "$(PROTO)" ]; then \
		echo "🔨 Found $(PROTO) in current directory"; \
		echo "🔨 Generating code for $(PROTO)..."; \
		mkdir -p $(OUTPUT_DIR); \
		trpc-open create \
			-p "$(PROTO)" \
			--rpconly \
			--nogomod \
			--mock=false \
			-o $(OUTPUT_DIR) || { \
			echo "❌ Error: trpc-open failed for $(PROTO)"; \
			exit 1; \
		}; \
		echo "✅ Generated code for $(PROTO)"; \
	else \
		echo "❌ Error: $(PROTO) not found in current directory"; \
		exit 1; \
	fi

# 列出可用的协议文件
.PHONY: list-available-proto
list-available-proto:
	@echo "📋 Available proto files in xData-mini:"
	@if [ -d "$(XDATA_PROTO_DIR)" ]; then \
		ls -la $(XDATA_PROTO_DIR)/*.proto 2>/dev/null | awk '{print "  " $$9}' || echo "  No .proto files found"; \
	else \
		echo "❌ xData-mini proto directory not found"; \
	fi
	@echo ""
	@echo "📋 Currently configured to sync:"
ifdef SYNC_ALL_PROTO
	@echo "  ALL proto files"
else
	@for file in $(SYNC_PROTO_FILES); do \
		echo "  $$file"; \
	done
endif

# 同步所有协议文件
.PHONY: sync-all-proto
sync-all-proto:
	@echo "📥 Syncing ALL proto files from xData-mini..."
	@if [ ! -d "$(XDATA_PROTO_DIR)" ]; then \
		echo "❌ Error: xData-mini proto directory not found at $(XDATA_PROTO_DIR)"; \
		exit 1; \
	fi
	@cp $(XDATA_PROTO_DIR)/*.proto ./ 2>/dev/null && \
		echo "✅ All proto files synced successfully!" || \
		echo "⚠️  No proto files found to sync"
	@echo "Synced files:"
	@ls -la *.proto 2>/dev/null | awk '{print "  " $$9}' || echo "  (none)"

# 同步所有协议文件并生成代码
.PHONY: sync-all-and-generate
sync-all-and-generate: sync-all-proto
	@echo "🔨 Generating code for all synced proto files..."
	@mkdir -p $(OUTPUT_DIR)
	@for proto_file in *.proto; do \
		if [ -f "$$proto_file" ]; then \
			echo "🔨 Processing $$proto_file..."; \
			trpc-open create \
				-p "$$proto_file" \
				--rpconly \
				--nogomod \
				--mock=false \
				-o $(OUTPUT_DIR) || { \
				echo "❌ Error: trpc-open failed for $$proto_file"; \
				exit 1; \
			}; \
			echo "✅ Generated code for $$proto_file"; \
		fi \
	done
	@echo "✅ All protobuf code generated successfully!"

# 检查协议文件差异
.PHONY: check-proto
check-proto:
	@echo "🔍 Checking proto files status..."
	@if [ ! -d "$(XDATA_PROTO_DIR)" ]; then \
		echo "❌ xData-mini proto directory not found"; \
		exit 1; \
	fi
	@need_update=false; \
	echo "Checking configured files:"; \
	for file in $(SYNC_PROTO_FILES); do \
		if [ -f "$(XDATA_PROTO_DIR)/$$file" ] && [ -f "$$file" ]; then \
			if ! cmp -s "$(XDATA_PROTO_DIR)/$$file" "$$file"; then \
				echo "  ⚠️  $$file is out of date"; \
				need_update=true; \
			else \
				echo "  ✅ $$file is up to date"; \
			fi \
		elif [ -f "$(XDATA_PROTO_DIR)/$$file" ]; then \
			echo "  ❌ $$file is missing locally"; \
			need_update=true; \
		else \
			echo "  ❌ $$file not found in source"; \
		fi \
	done; \
	if [ "$$need_update" = "true" ]; then \
		echo ""; \
		echo "💡 Run 'make sync-proto generate' to update and regenerate"; \
	fi

# 验证环境
.PHONY: check-env
check-env:
	@echo "🔍 Checking environment..."
	@command -v trpc-open >/dev/null 2>&1 || { \
		echo "❌ Error: trpc-open is not installed or not in PATH"; \
		exit 1; \
	}
	@echo "✅ trpc-open is available"
	@if [ ! -d "$(XDATA_PROTO_DIR)" ]; then \
		echo "⚠️  Warning: xData-mini proto directory not found at $(XDATA_PROTO_DIR)"; \
	else \
		echo "✅ xData-mini proto directory found"; \
	fi

# 显示状态
.PHONY: status
status:
	@echo "📊 Proto Build Status"
	@echo "====================="
	@echo "Source directory: $(XDATA_PROTO_DIR)"
	@echo "Working directory: $(LOCAL_PROTO_DIR)"
	@echo "Output directory: $(OUTPUT_DIR)"
	@echo ""
	@echo "📋 Local proto files:"
	@for file in $(LOCAL_PROTO_FILES); do \
		if [ -f "$$file" ]; then \
			echo "  ✅ $$file (local)"; \
		else \
			echo "  ❌ $$file (missing)"; \
		fi \
	done
	@echo ""
	@echo "📋 Synced proto files:"
	@for file in $(SYNC_PROTO_FILES); do \
		if [ -f "$$file" ]; then \
			echo "  ✅ $$file (synced)"; \
		else \
			echo "  ❌ $$file (not synced)"; \
		fi \
	done
	@echo ""
	@echo "📁 All proto files in current directory:"
	@ls -la *.proto 2>/dev/null | awk '{print "  " $$9 " (" $$5 " bytes)"}' || echo "  (none)"
	@echo ""
	@if [ -d "$(OUTPUT_DIR)" ]; then \
		echo "🔨 Generated files:"; \
		find $(OUTPUT_DIR) -name "*.go" 2>/dev/null | head -10 | sed 's/^/  /' || echo "  (none)"; \
		generated_count=$$(find $(OUTPUT_DIR) -name "*.go" 2>/dev/null | wc -l); \
		echo "  Total: $$generated_count Go files"; \
	else \
		echo "🔨 No generated files found"; \
	fi

# 清理
.PHONY: clean
clean:
	@echo "🧹 Cleaning generated files..."
	@rm -rf $(OUTPUT_DIR)
	@echo "✅ Cleaned generated files"

.PHONY: clean-all
clean-all: clean
	@echo "🧹 Cleaning synced proto files..."
	@rm -rf $(LOCAL_PROTO_DIR)
	@echo "✅ Cleaned all files"

# 帮助信息
.PHONY: help
help:
	@echo "📖 Available targets:"
	@echo "  all                    - Complete build process (default)"
	@echo "  sync-proto             - Sync configured proto files from xData-mini"
	@echo "  generate-local         - Generate code for local proto files"
	@echo "  generate-synced        - Generate code for synced proto files"  
	@echo "  generate               - Generate code for all proto files (local + synced)"
	@echo "  sync-all-proto         - Sync ALL proto files from xData-mini"
	@echo "  sync-all-and-generate  - Sync all proto files and generate code"
	@echo "  generate-single        - Generate code for single proto file"
	@echo "                           Usage: make generate-single PROTO=filename.proto"
	@echo "                           (searches in current directory)"
	@echo "  list-available-proto   - List available proto files"
	@echo "  check-proto            - Check if proto files need update"
	@echo "  check-env              - Check environment and dependencies"
	@echo "  status                 - Show current status"
	@echo "  clean                  - Clean generated files"
	@echo "  clean-all              - Clean all files"
	@echo "  help                   - Show this help"
	@echo ""
	@echo "🚀 Quick start:"
	@echo "  make all                         - Build everything (recommended)"
	@echo "  make generate-local              - Generate local proto files only"
	@echo "  make generate-single PROTO=moox.proto  - Generate single file"
	@echo "  make sync-all-and-generate       - Sync all and generate"
