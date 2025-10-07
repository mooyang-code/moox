#!/bin/bash

# 发布脚本 - 压缩release目录并上传到远程服务器

# 配置变量
RELEASE_DIR="/Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/server/release"
ZIP_FILE="moox.zip"
REMOTE_SERVER="" # 请填写远程服务器地址，例如: user@192.168.1.100
REMOTE_DIR="" # 将根据用户名动态设置

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
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

# 检查必要参数
if [ -z "$1" ]; then
    print_error "请提供远程服务器地址作为参数"
    echo "使用方法: $0 <远程服务器地址>"
    echo "例如: $0 user@192.168.1.100"
    exit 1
fi

REMOTE_SERVER="$1"

# 从服务器地址中提取用户名
REMOTE_USER=$(echo "$REMOTE_SERVER" | cut -d'@' -f1)
if [ -z "$REMOTE_USER" ] || [ "$REMOTE_USER" = "$REMOTE_SERVER" ]; then
    print_error "无效的服务器地址格式，请使用 user@host 格式"
    exit 1
fi

# 更新远程目录为用户的home目录
REMOTE_DIR="/home/$REMOTE_USER"
print_info "目标目录: $REMOTE_DIR"

# 检查release目录是否存在
if [ ! -d "$RELEASE_DIR" ]; then
    print_error "Release目录不存在: $RELEASE_DIR"
    exit 1
fi

# 切换到release目录的父目录
cd "$(dirname "$RELEASE_DIR")" || exit 1

# 创建临时目录并复制文件（重命名为moox）
TEMP_DIR=$(mktemp -d)
print_info "创建临时目录: $TEMP_DIR"
cp -r "$RELEASE_DIR" "$TEMP_DIR/moox"

# 切换到临时目录
cd "$TEMP_DIR" || exit 1

# 压缩moox目录（排除log和data文件夹）
print_info "开始压缩 moox 目录（排除 log 和 data 文件夹）..."
if zip -r "$ZIP_FILE" "moox" -x "moox/log/*" "moox/data/*" > /dev/null 2>&1; then
    print_success "压缩完成: $ZIP_FILE"
    print_info "压缩文件大小: $(du -h "$ZIP_FILE" | cut -f1)"
    # 将压缩文件移回原目录
    mv "$ZIP_FILE" "$(dirname "$RELEASE_DIR")/"
    cd "$(dirname "$RELEASE_DIR")" || exit 1
else
    print_error "压缩失败"
    rm -rf "$TEMP_DIR"
    exit 1
fi

# 清理临时目录
rm -rf "$TEMP_DIR"

# 上传到远程服务器
print_info "开始上传到远程服务器: $REMOTE_SERVER"
if scp "$ZIP_FILE" "$REMOTE_SERVER:$REMOTE_DIR/"; then
    print_success "文件已成功上传到 $REMOTE_SERVER:$REMOTE_DIR/$ZIP_FILE"
else
    print_error "上传失败"
    # 清理本地压缩文件
    rm -f "$ZIP_FILE"
    exit 1
fi

# 在远程服务器上解压文件
print_info "在远程服务器上解压文件..."
ssh "$REMOTE_SERVER" "cd $REMOTE_DIR && unzip -o $ZIP_FILE && rm -f $ZIP_FILE" 2>/dev/null
if [ $? -eq 0 ]; then
    print_success "远程解压完成"
    
    # 启动服务
    print_info "启动远程服务..."
    ssh "$REMOTE_SERVER" "cd $REMOTE_DIR/moox && chmod +x start.sh && ./start.sh" 2>&1
    if [ $? -eq 0 ]; then
        print_success "服务启动成功"
    else
        print_warning "服务启动可能失败，请检查"
    fi
else
    print_warning "远程解压可能失败，请检查"
fi

# 清理本地压缩文件
print_info "清理本地压缩文件..."
rm -f "$ZIP_FILE"
print_success "发布完成!"