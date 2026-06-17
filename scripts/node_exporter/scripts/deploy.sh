#!/bin/bash

# node_exporter 部署脚本 - 部署到远程服务器并自动重启

set -e

# 配置变量
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
BUILD_DIR="$PROJECT_DIR/build"
APP_NAME="node_exporter"

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

print_success() { echo -e "${GREEN}[SUCCESS]${NC} $1"; }
print_error()   { echo -e "${RED}[ERROR]${NC} $1"; }
print_info()    { echo -e "${YELLOW}[INFO]${NC} $1"; }

print_usage() {
    echo "使用方法:"
    echo "  $0 <user@host> [arch] [version] [port]"
    echo ""
    echo "参数说明:"
    echo "  user@host  : 远程服务器地址 (必填)"
    echo "  arch       : 架构, amd64 或 arm64 (默认: amd64)"
    echo "  version    : node_exporter 版本 (默认: 1.10.2)"
    echo "  port       : 监听端口 (默认: 9100)"
    echo ""
    echo "示例:"
    echo "  $0 root@10.0.0.1"
    echo "  $0 root@10.0.0.1 arm64"
    echo "  $0 root@10.0.0.1 amd64 1.10.2 9200"
}

# 参数解析
if [ -z "$1" ]; then
    print_error "缺少服务器地址"
    print_usage
    exit 1
fi

REMOTE_SERVER="$1"
ARCH="${2:-amd64}"
VERSION="${3:-1.10.2}"
LISTEN_PORT="${4:-9100}"
REMOTE_DIR="~/node_exporter"

# 验证服务器地址格式
REMOTE_USER=$(echo "$REMOTE_SERVER" | cut -d'@' -f1)
if [ -z "$REMOTE_USER" ] || [ "$REMOTE_USER" = "$REMOTE_SERVER" ]; then
    print_error "无效的服务器地址格式，请使用 user@host 格式"
    exit 1
fi

# 检查本地二进制是否存在，不存在则先构建
BINARY="$BUILD_DIR/$APP_NAME"
if [ ! -f "$BINARY" ]; then
    print_info "本地二进制不存在，开始下载..."
    cd "$PROJECT_DIR" && make "build-linux-${ARCH}" VERSION="$VERSION"
fi

if [ ! -f "$BINARY" ]; then
    print_error "构建失败，二进制文件不存在: $BINARY"
    exit 1
fi

print_info "部署配置:"
print_info "  目标服务器: $REMOTE_SERVER"
print_info "  架构: $ARCH"
print_info "  版本: $VERSION"
print_info "  监听端口: $LISTEN_PORT"
print_info "  远程目录: $REMOTE_DIR"

# 1. 创建远程目录
print_info "创建远程目录..."
ssh "$REMOTE_SERVER" "mkdir -p $REMOTE_DIR"

# 2. 停止远程已运行的 node_exporter
print_info "停止远程已运行的 node_exporter..."
ssh "$REMOTE_SERVER" << 'STOP_EOF'
# 查找并停止 node_exporter 进程
PIDS=$(pgrep -f "node_exporter" 2>/dev/null || true)
if [ -n "$PIDS" ]; then
    echo "发现运行中的 node_exporter 进程: $PIDS"
    echo "正在停止..."
    kill $PIDS 2>/dev/null || true
    sleep 2
    # 如果还在运行，强制终止
    PIDS=$(pgrep -f "node_exporter" 2>/dev/null || true)
    if [ -n "$PIDS" ]; then
        echo "强制终止进程..."
        kill -9 $PIDS 2>/dev/null || true
        sleep 1
    fi
    echo "已停止"
else
    echo "没有运行中的 node_exporter 进程"
fi
STOP_EOF

# 3. 备份旧版本
print_info "备份旧版本..."
ssh "$REMOTE_SERVER" << EOF
if [ -f $REMOTE_DIR/$APP_NAME ]; then
    cp $REMOTE_DIR/$APP_NAME $REMOTE_DIR/${APP_NAME}.bak
    echo "已备份为 ${APP_NAME}.bak"
else
    echo "无旧版本需要备份"
fi
EOF

# 4. 上传二进制文件
print_info "上传二进制文件到远程服务器..."
if scp "$BINARY" "$REMOTE_SERVER:$REMOTE_DIR/$APP_NAME"; then
    print_success "上传完成"
else
    print_error "上传失败"
    exit 1
fi

# 5. 远程启动服务
print_info "启动 node_exporter..."
ssh "$REMOTE_SERVER" << EOF
set -e

cd $REMOTE_DIR
chmod +x $APP_NAME

# 启动 node_exporter
nohup ./$APP_NAME --web.listen-address=":${LISTEN_PORT}" > /dev/null 2>&1 &
NEW_PID=\$!
echo "\$NEW_PID" > $REMOTE_DIR/${APP_NAME}.pid
echo "node_exporter 已启动 (PID: \$NEW_PID, 端口: ${LISTEN_PORT})"

# 等待进程启动
sleep 2

# 验证进程是否在运行
if ps -p \$NEW_PID > /dev/null 2>&1; then
    echo "进程运行正常"
else
    echo "进程启动失败，请检查日志"
    exit 1
fi
EOF

if [ $? -eq 0 ]; then
    print_success "部署完成!"
    print_info "远程目录: $REMOTE_DIR"
    print_info "监听端口: $LISTEN_PORT"
    print_info "验证命令: curl http://<host>:${LISTEN_PORT}/metrics"
else
    print_error "部署过程中出现错误"
    exit 1
fi
