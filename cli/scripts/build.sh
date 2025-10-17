#!/bin/bash

# MooX CLI 构建脚本
# 使用方法: ./build.sh [版本号]

set -e  # 遇到错误立即退出

# 颜色输出函数
print_info() {
    echo -e "\033[34m[INFO]\033[0m $1"
}

print_success() {
    echo -e "\033[32m[SUCCESS]\033[0m $1"
}

print_error() {
    echo -e "\033[31m[ERROR]\033[0m $1"
}

print_warning() {
    echo -e "\033[33m[WARNING]\033[0m $1"
}

# 获取版本号
VERSION=${1:-"dev"}
BUILD_TIME=$(date +"%Y-%m-%d %H:%M:%S")
GIT_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")

print_info "开始构建 MooX CLI..."
print_info "版本: $VERSION"
print_info "构建时间: $BUILD_TIME"
print_info "Git提交: $GIT_COMMIT"

# 定义路径变量
SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
PROJECT_ROOT=$(cd "$SCRIPT_DIR/.." && pwd)
RELEASE_DIR="$PROJECT_ROOT/release"
APP_NAME="moox"
BUILD_DIR="$RELEASE_DIR"

print_info "脚本目录: $SCRIPT_DIR"  
print_info "项目根目录: $PROJECT_ROOT"
print_info "构建目录: $BUILD_DIR"

# 切换到项目根目录
cd "$PROJECT_ROOT"

# 创建构建目录
print_info "创建构建目录..."
mkdir -p "$BUILD_DIR"
mkdir -p "$BUILD_DIR/bin"
mkdir -p "$BUILD_DIR/config"
mkdir -p "$BUILD_DIR/examples"

# 清理旧文件
print_info "清理旧的构建文件..."
rm -f "$BUILD_DIR/bin/$APP_NAME"
rm -f "$BUILD_DIR/$APP_NAME"  # 清理旧的根目录下的二进制文件

# 构建二进制文件
print_info "开始编译二进制文件..."
export CGO_ENABLED=1

# 添加构建信息
LDFLAGS="-X 'main.Version=$VERSION' -X 'main.BuildTime=$BUILD_TIME' -X 'main.GitCommit=$GIT_COMMIT'"

# 构建不同平台的二进制文件
platforms=("linux/amd64" "darwin/amd64" "darwin/arm64" "windows/amd64")

for platform in "${platforms[@]}"; do
    platform_split=(${platform//\// })
    GOOS=${platform_split[0]}
    GOARCH=${platform_split[1]}
    
    output_name="$APP_NAME"
    if [ $GOOS = "windows" ]; then
        output_name="$APP_NAME.exe"
    fi
    
    # 为当前平台构建
    if [[ "$GOOS" == "$(go env GOOS)" && "$GOARCH" == "$(go env GOARCH)" ]]; then
        print_info "构建当前平台 ($GOOS/$GOARCH)..."
        if GOOS=$GOOS GOARCH=$GOARCH go build -ldflags "$LDFLAGS" -o "$BUILD_DIR/bin/$output_name" .; then
            print_success "当前平台二进制文件编译成功: $BUILD_DIR/bin/$output_name"
        else
            print_error "当前平台二进制文件编译失败"
            exit 1
        fi
    else
        # 其他平台构建到release目录
        platform_dir="$BUILD_DIR/$GOOS-$GOARCH"
        mkdir -p "$platform_dir"
        print_info "构建 $GOOS/$GOARCH 平台..."
        if GOOS=$GOOS GOARCH=$GOARCH go build -ldflags "$LDFLAGS" -o "$platform_dir/$output_name" .; then
            print_success "$GOOS/$GOARCH 平台编译成功: $platform_dir/$output_name"
        else
            print_warning "$GOOS/$GOARCH 平台编译失败，跳过"
        fi
    fi
done

# 拷贝配置文件
print_info "拷贝配置文件..."
if [ -d "config" ]; then
    cp config/*.yaml "$BUILD_DIR/config/" 2>/dev/null || true
    if [ $? -eq 0 ]; then
        print_success "配置文件拷贝成功"
        ls -la "$BUILD_DIR/config/"
    else
        print_warning "没有找到YAML配置文件"
    fi
else
    print_warning "config目录不存在"
fi

# 额外拷贝配置文件到bin目录（与二进制文件同级）
print_info "拷贝配置文件到bin目录..."
if [ -f "config/cli-example.yaml" ]; then
    cp config/cli-example.yaml "$BUILD_DIR/bin/cli-example.yaml"
    print_success "cli-example.yaml 已拷贝到 bin 目录"
fi
if [ -f "config/cli.yaml" ]; then
    cp config/cli.yaml "$BUILD_DIR/bin/cli.yaml"
    print_success "cli.yaml 已拷贝到 bin 目录"
fi

# 创建使用脚本
print_info "创建使用脚本..."

# 创建安装脚本
cat > "$BUILD_DIR/install.sh" << EOF
#!/bin/bash

# MooX CLI 安装脚本

APP_NAME="$APP_NAME"
INSTALL_DIR="/usr/local/bin"

print_info() {
    echo -e "\033[34m[INFO]\033[0m \$1"
}

print_success() {
    echo -e "\033[32m[SUCCESS]\033[0m \$1"
}

print_error() {
    echo -e "\033[31m[ERROR]\033[0m \$1"
}

# 检查是否有管理员权限
if [ "\$EUID" -ne 0 ]; then
    print_error "请使用sudo运行此脚本"
    exit 1
fi

# 检查二进制文件是否存在
if [ ! -f "bin/\$APP_NAME" ]; then
    print_error "二进制文件不存在: bin/\$APP_NAME"
    exit 1
fi

# 拷贝二进制文件到系统路径
print_info "安装 \$APP_NAME 到 \$INSTALL_DIR..."
cp "bin/\$APP_NAME" "\$INSTALL_DIR/"
chmod +x "\$INSTALL_DIR/\$APP_NAME"

# 创建配置目录
CONFIG_DIR="/etc/moox"
mkdir -p "\$CONFIG_DIR"
cp config/cli-example.yaml "\$CONFIG_DIR/cli.yaml"

print_success "\$APP_NAME 安装成功！"
print_info "二进制文件: \$INSTALL_DIR/\$APP_NAME"
print_info "配置文件: \$CONFIG_DIR/cli.yaml"
print_info "使用方法: \$APP_NAME --help"
EOF

# 创建卸载脚本
cat > "$BUILD_DIR/uninstall.sh" << EOF
#!/bin/bash

# MooX CLI 卸载脚本

APP_NAME="$APP_NAME"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/moox"

print_info() {
    echo -e "\033[34m[INFO]\033[0m \$1"
}

print_success() {
    echo -e "\033[32m[SUCCESS]\033[0m \$1"
}

# 检查是否有管理员权限
if [ "\$EUID" -ne 0 ]; then
    echo "请使用sudo运行此脚本"
    exit 1
fi

# 删除二进制文件
if [ -f "\$INSTALL_DIR/\$APP_NAME" ]; then
    print_info "删除二进制文件..."
    rm -f "\$INSTALL_DIR/\$APP_NAME"
fi

# 删除配置目录
if [ -d "\$CONFIG_DIR" ]; then
    print_info "删除配置目录..."
    rm -rf "\$CONFIG_DIR"
fi

print_success "\$APP_NAME 卸载完成！"
EOF

# 设置脚本执行权限
chmod +x "$BUILD_DIR/install.sh"
chmod +x "$BUILD_DIR/uninstall.sh"
chmod +x "$BUILD_DIR/bin/$APP_NAME"

# 创建README文件
print_info "创建使用说明..."
cat > "$BUILD_DIR/README.md" << EOF
# MooX CLI $VERSION

## 简介

MooX CLI 是一个多功能的命令行工具，支持数据库操作、存储服务和用户认证等功能。

## 安装

### 方法一：直接运行（推荐开发使用）

\`\`\`bash
# 运行二进制文件
./bin/$APP_NAME --help

# 或者将bin目录添加到PATH
export PATH=\$PATH:\$(pwd)/bin
$APP_NAME --help
\`\`\`

### 方法二：系统安装（推荐生产使用）

\`\`\`bash
# 安装到系统目录（需要sudo权限）
sudo ./install.sh

# 全局使用
$APP_NAME --help

# 卸载
sudo ./uninstall.sh
\`\`\`

## 配置

拷贝并修改配置文件：

\`\`\`bash
# 开发环境
cp config/cli-example.yaml config/cli.yaml

# 生产环境
sudo cp config/cli-example.yaml /etc/moox/cli.yaml
\`\`\`

## 主要功能

### 用户认证
\`\`\`bash
# 用户注册
$APP_NAME auth register

# 命令行参数注册
$APP_NAME auth register --username myuser --password mypass --nickname "我的昵称"
\`\`\`

### 数据库操作
\`\`\`bash
# 查看数据库命令
$APP_NAME db --help
\`\`\`

### 存储服务
\`\`\`bash
# 查看存储命令
$APP_NAME storage --help
\`\`\`

### 消息队列
\`\`\`bash
# 查看消息命令
$APP_NAME msg --help
\`\`\`

## 多平台支持

构建包含以下平台的二进制文件：
- Linux x64 (linux-amd64/)
- macOS Intel (darwin-amd64/)
- macOS Apple Silicon (darwin-arm64/)
- Windows x64 (windows-amd64/)

## 构建信息

- 版本: $VERSION
- 构建时间: $BUILD_TIME
- Git提交: $GIT_COMMIT

## 技术栈

- Go $GO_VERSION
- Cobra CLI框架
- YAML配置管理
- HTTP客户端
- CGO支持

## 支持

如有问题，请查看：
- 帮助信息: \`$APP_NAME --help\`
- 配置示例: \`config/cli-example.yaml\`
- 项目仓库: https://github.com/mooyang-code/moox
EOF

# 创建版本信息文件
cat > "$BUILD_DIR/VERSION" << EOF
VERSION=$VERSION
BUILD_TIME=$BUILD_TIME
GIT_COMMIT=$GIT_COMMIT
GO_VERSION=$(go version | awk '{print $3}')
EOF

print_success "构建完成！"
print_info "构建目录: $BUILD_DIR"
print_info "二进制文件: $BUILD_DIR/bin/$APP_NAME"
print_info "配置文件: $BUILD_DIR/config/"
print_info "使用方法: cd $BUILD_DIR && ./bin/$APP_NAME --help"
print_info "系统安装: cd $BUILD_DIR && sudo ./install.sh"

# 显示文件列表
print_info "构建产物:"
ls -la "$BUILD_DIR/"
print_info "二进制文件:"
ls -la "$BUILD_DIR/bin/" 