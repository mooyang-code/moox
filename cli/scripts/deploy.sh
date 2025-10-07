#!/bin/bash

# Moox CLI 发布脚本 - 支持多平台部署

# 配置变量
# 获取脚本所在目录的父目录（项目根目录）
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
RELEASE_BASE_DIR="$PROJECT_DIR/release"
PROJECT_NAME="moox-cli"
REMOTE_SERVER=""
REMOTE_DIR=""
PLATFORM=""

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# 打印带颜色的消息
print_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

print_info() {
    echo -e "${YELLOW}[INFO]${NC} $1"
}

print_usage() {
    echo -e "${BLUE}使用方法:${NC}"
    echo "  $0 <服务器地址> [平台]"
    echo ""
    echo "参数说明:"
    echo "  服务器地址: localhost 或 user@host 格式"
    echo "  平台: linux, darwin/macos (可选，不指定则自动检测)"
    echo ""
    echo "示例:"
    echo "  $0 localhost                    # 本地部署，自动检测平台"
    echo "  $0 localhost darwin             # 本地部署macOS版本"
    echo "  $0 user@192.168.1.100          # 远程部署，自动检测平台"
    echo "  $0 user@192.168.1.100 linux    # 远程部署Linux版本"
}

# 检测系统平台
detect_platform() {
    local target=$1
    
    if [ "$target" = "localhost" ]; then
        # 本地系统检测
        local os=$(uname -s | tr '[:upper:]' '[:lower:]')
    else
        # 远程系统检测
        local os=$(ssh "$target" "uname -s 2>/dev/null" | tr '[:upper:]' '[:lower:]')
    fi
    
    case "$os" in
        "linux")
            echo "linux"
            ;;
        "darwin")
            echo "darwin"
            ;;
        *)
            >&2 echo "无法检测系统平台或不支持的平台: $os"
            exit 1
            ;;
    esac
}

# 检查必要参数
if [ -z "$1" ]; then
    print_error "缺少必要参数"
    print_usage
    exit 1
fi

REMOTE_SERVER="$1"

# 判断是本地还是远程部署
if [ "$REMOTE_SERVER" = "localhost" ]; then
    IS_LOCAL=true
    REMOTE_USER=$(whoami)
    print_info "本地部署模式"
else
    IS_LOCAL=false
    # 从服务器地址中提取用户名
    REMOTE_USER=$(echo "$REMOTE_SERVER" | cut -d'@' -f1)
    if [ -z "$REMOTE_USER" ] || [ "$REMOTE_USER" = "$REMOTE_SERVER" ]; then
        print_error "无效的服务器地址格式，请使用 user@host 格式"
        exit 1
    fi
    print_info "远程部署模式: $REMOTE_SERVER"
fi

# 设置远程部署目录
REMOTE_DIR="~/moox/cli"
REMOTE_DATA_DIR="~/moox/data"

# 处理平台参数
if [ -n "$2" ]; then
    # 用户指定了平台
    case "$2" in
        "linux"|"darwin"|"macos")
            PLATFORM="$2"
            if [ "$PLATFORM" = "macos" ]; then
                PLATFORM="darwin"
            fi
            ;;
        *)
            print_error "不支持的平台: $2"
            print_info "支持的平台: linux, darwin, macos"
            exit 1
            ;;
    esac
    print_info "使用指定平台: $PLATFORM"
else
    # 自动检测平台
    print_info "检测系统平台..."
    PLATFORM=$(detect_platform "$REMOTE_SERVER")
    print_success "检测到平台: $PLATFORM"
fi

# 构建平台特定的release目录
RELEASE_DIR="$RELEASE_BASE_DIR/$PLATFORM"
ZIP_FILE="${PROJECT_NAME}-${PLATFORM}.zip"

# 检查release目录是否存在
if [ ! -d "$RELEASE_DIR" ]; then
    print_error "平台 $PLATFORM 的 release 目录不存在: $RELEASE_DIR"
    print_info "请先运行 make build-$PLATFORM 构建该平台的二进制文件"
    exit 1
fi

print_info "准备部署 $PLATFORM 平台版本"
print_info "源目录: $RELEASE_DIR"
if [ "$IS_LOCAL" = true ]; then
    print_info "目标: 本地部署"
else
    print_info "目标服务器: $REMOTE_SERVER"
fi
print_info "目标目录: $REMOTE_DIR"

if [ "$IS_LOCAL" = true ]; then
    # 本地部署，直接复制文件
    print_info "准备本地部署..."
else
    # 远程部署，需要压缩
    # 切换到release目录的父目录
    cd "$RELEASE_BASE_DIR" || exit 1
    
    # 创建临时目录
    TEMP_DIR=$(mktemp -d)
    print_info "创建临时目录: $TEMP_DIR"
    
    # 复制文件到临时目录（保持目录名为 cli）
    cp -r "$PLATFORM" "$TEMP_DIR/cli"
    
    # 切换到临时目录
    cd "$TEMP_DIR" || exit 1
    
    # 压缩目录（排除data文件夹）
    print_info "开始压缩 cli 目录（排除 data 文件夹）..."
    if zip -r "$ZIP_FILE" "cli" -x "cli/data/*" > /dev/null 2>&1; then
        print_success "压缩完成: $ZIP_FILE"
        print_info "压缩文件大小: $(du -h "$ZIP_FILE" | cut -f1)"
        # 将压缩文件移回release目录
        mv "$ZIP_FILE" "$RELEASE_BASE_DIR/"
        cd "$RELEASE_BASE_DIR" || exit 1
    else
        print_error "压缩失败"
        rm -rf "$TEMP_DIR"
        exit 1
    fi
    
    # 清理临时目录
    rm -rf "$TEMP_DIR"
fi

if [ "$IS_LOCAL" = true ]; then
    # 本地部署
    print_info "开始本地部署..."
    
    # 创建目标目录
    mkdir -p ~/moox/cli ~/moox/data
    
    # 备份旧版本（如果存在）
    if [ -d ~/moox/cli ]; then
        print_info "备份旧版本..."
        rm -rf ~/moox/cli.bak
        mv ~/moox/cli ~/moox/cli.bak
    fi
    
    # 复制新版本
    print_info "复制新版本到 ~/moox/cli..."
    cp -r "$RELEASE_DIR" ~/moox/cli
    
    # 处理后续部署步骤
    cd ~/moox/cli || exit 1
    
    # 创建指向共享数据目录的软链接
    print_info "创建数据目录链接..."
    if [ -d data ]; then
        rm -rf data
    fi
    ln -s ~/moox/data data
    
    # 设置执行权限
    chmod +x bin/*
    
    # 创建软链接到用户 bin 目录
    print_info "创建命令行工具链接..."
    mkdir -p ~/bin
    ln -sf ~/moox/cli/bin/$PROJECT_NAME ~/bin/$PROJECT_NAME
    
    # 验证安装
    print_info "验证安装..."
    if ~/bin/$PROJECT_NAME --help > /dev/null 2>&1; then
        print_success "$PROJECT_NAME 已成功部署"
    else
        print_error "无法运行 $PROJECT_NAME --help"
        exit 1
    fi
    
    print_success "本地部署完成!"
    print_info "CLI 工具目录: ~/moox/cli"
    print_info "可执行文件: ~/bin/$PROJECT_NAME"
    print_info "共享数据目录: ~/moox/data"
    print_info ""
    print_info "使用方式: $PROJECT_NAME --help"
    
else
    # 远程部署
    # 创建远程目录
    print_info "创建远程目录结构..."
    ssh "$REMOTE_SERVER" "mkdir -p ~/moox/cli ~/moox/data"
    
    # 上传到远程服务器
    print_info "开始上传到远程服务器: $REMOTE_SERVER"
    if scp "$ZIP_FILE" "$REMOTE_SERVER:/tmp/"; then
        print_success "文件已成功上传到 $REMOTE_SERVER:/tmp/$ZIP_FILE"
    else
        print_error "上传失败"
        rm -f "$ZIP_FILE"
        exit 1
    fi
    
    # 在远程服务器上部署 CLI 工具
    print_info "在远程服务器上部署 CLI 工具..."
    ssh "$REMOTE_SERVER" << EOF
set -e

# 备份旧版本（如果存在）
if [ -d ~/moox/cli ]; then
    echo "备份旧版本..."
    rm -rf ~/moox/cli.bak
    mv ~/moox/cli ~/moox/cli.bak
fi

# 解压新版本
echo "解压新版本到 ~/moox..."
cd ~/moox
unzip -q -o /tmp/$ZIP_FILE
rm -f /tmp/$ZIP_FILE

# 创建指向共享数据目录的软链接
echo "创建数据目录链接..."
cd ~/moox/cli
if [ -d data ]; then
    rm -rf data
fi
ln -s ~/moox/data data

# 设置执行权限
chmod +x bin/*

# 创建软链接到用户 bin 目录
echo "创建命令行工具链接..."
mkdir -p ~/bin
ln -sf ~/moox/cli/bin/$PROJECT_NAME ~/bin/$PROJECT_NAME

# 验证安装
echo ""
echo "验证安装..."
if ~/bin/$PROJECT_NAME --version; then
    echo "✅ $PROJECT_NAME 已成功部署"
else
    echo "⚠️  警告：无法运行 $PROJECT_NAME --version"
fi
EOF
    
    if [ $? -eq 0 ]; then
        print_success "远程部署完成!"
        print_info "CLI 工具目录: ~/moox/cli"
        print_info "可执行文件: ~/bin/$PROJECT_NAME"
        print_info "共享数据目录: ~/moox/data"
        print_info ""
        print_info "使用方式: ssh $REMOTE_SERVER '$PROJECT_NAME --help'"
    else
        print_error "部署过程中出现错误"
    fi
    
    # 清理本地压缩文件
    print_info "清理本地压缩文件..."
    rm -f "$ZIP_FILE"
fi