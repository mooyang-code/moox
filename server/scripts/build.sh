#!/bin/bash

# Moox Server 构建脚本
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

print_info "开始构建 Moox Server..."
print_info "版本: $VERSION"
print_info "构建时间: $BUILD_TIME"
print_info "Git提交: $GIT_COMMIT"

# 定义路径变量
SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
PROJECT_ROOT=$(cd "$SCRIPT_DIR/.." && pwd)
RELEASE_DIR="$PROJECT_ROOT/release"
APP_NAME="moox-server"
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
mkdir -p "$BUILD_DIR/log"
mkdir -p "$BUILD_DIR/data"

# 清理旧文件
print_info "清理旧的构建文件..."
rm -f "$BUILD_DIR/bin/$APP_NAME"
rm -f "$BUILD_DIR/$APP_NAME"  # 清理旧的根目录下的二进制文件
rm -f "$BUILD_DIR/config"/*.yaml

# 构建二进制文件
print_info "开始编译二进制文件..."
export CGO_ENABLED=1

# 添加构建信息
LDFLAGS="-X 'main.Version=$VERSION' -X 'main.BuildTime=$BUILD_TIME' -X 'main.GitCommit=$GIT_COMMIT'"

if go build -ldflags "$LDFLAGS" -o "$BUILD_DIR/bin/$APP_NAME" .; then
    print_success "二进制文件编译成功: $BUILD_DIR/bin/$APP_NAME"
else
    print_error "二进制文件编译失败"
    exit 1
fi

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

# 创建启动脚本
print_info "创建启动脚本..."
cat > "$BUILD_DIR/start.sh" << EOF
#!/bin/bash

# Moox Server 启动脚本

APP_NAME="$APP_NAME"
PID_FILE="./\$APP_NAME.pid"

# 检查并停止已存在的进程
echo "检查已存在的进程..."

# 检查PID文件中的进程
if [ -f "\$PID_FILE" ]; then
    OLD_PID=\$(cat "\$PID_FILE")
    if ps -p "\$OLD_PID" > /dev/null 2>&1; then
        echo "发现运行中的服务 (PID: \$OLD_PID)，正在停止..."
        kill "\$OLD_PID"
        
        # 等待进程结束
        for i in {1..10}; do
            if ! ps -p "\$OLD_PID" > /dev/null 2>&1; then
                echo "旧进程已停止"
                break
            fi
            sleep 1
        done
        
        # 如果还在运行，强制杀死
        if ps -p "\$OLD_PID" > /dev/null 2>&1; then
            echo "强制停止旧进程..."
            kill -9 "\$OLD_PID"
        fi
    fi
    rm -f "\$PID_FILE"
fi

# 通过进程名查找并停止可能的进程
RUNNING_PIDS=\$(pgrep -f "\$APP_NAME" 2>/dev/null || true)
if [ ! -z "\$RUNNING_PIDS" ]; then
    echo "发现其他运行中的 \$APP_NAME 进程: \$RUNNING_PIDS"
    echo "正在停止这些进程..."
    echo "\$RUNNING_PIDS" | xargs kill 2>/dev/null || true
    sleep 2
    
    # 强制杀死仍在运行的进程
    STILL_RUNNING=\$(pgrep -f "\$APP_NAME" 2>/dev/null || true)
    if [ ! -z "\$STILL_RUNNING" ]; then
        echo "强制停止残留进程: \$STILL_RUNNING"
        echo "\$STILL_RUNNING" | xargs kill -9 2>/dev/null || true
    fi
fi

# 数据库初始化
echo "检查数据库状态..."
cd ./bin
if [ ! -f "../data/auth.db" ]; then
    echo "数据库不存在，正在初始化..."
    if [ -f "../sql/schema.sql" ]; then
        cd ../data && sqlite3 auth.db < ../sql/schema.sql && echo "数据库初始化完成"
        cd ../bin
    else
        echo "警告: SQL schema文件不存在，跳过数据库初始化"
    fi
else
    echo "数据库已存在，跳过初始化"
fi

# 启动服务
echo "启动 \$APP_NAME..."
nohup ./\$APP_NAME -conf=../config/trpc_go.yaml > ../log/app.log 2>&1 &
echo \$! > "../\$PID_FILE"
echo "#服务已启动 (PID: \$(cat ../\$PID_FILE))"
echo "#日志文件: log/app.log"
EOF

# 创建停止脚本
cat > "$BUILD_DIR/stop.sh" << EOF
#!/bin/bash

# Moox Server 停止脚本

APP_NAME="$APP_NAME"
PID_FILE="./\$APP_NAME.pid"

if [ ! -f "\$PID_FILE" ]; then
    echo "PID文件不存在，服务可能没有运行"
    exit 1
fi

PID=\$(cat "\$PID_FILE")

if ps -p "\$PID" > /dev/null 2>&1; then
    echo "停止服务 (PID: \$PID)..."
    kill "\$PID"
    
    # 等待进程结束
    for i in {1..10}; do
        if ! ps -p "\$PID" > /dev/null 2>&1; then
            echo "服务已停止"
            rm -f "\$PID_FILE"
            exit 0
        fi
        sleep 1
    done
    
    # 强制杀死进程
    echo "强制停止服务..."
    kill -9 "\$PID"
    rm -f "\$PID_FILE"
else
    echo "服务没有运行"
    rm -f "\$PID_FILE"
fi
EOF

# 设置脚本执行权限
chmod +x "$BUILD_DIR/start.sh"
chmod +x "$BUILD_DIR/stop.sh"
chmod +x "$BUILD_DIR/bin/$APP_NAME"

# 创建README文件
print_info "创建使用说明..."
cat > "$BUILD_DIR/README.md" << EOF
# Moox Server

## 构建信息
- 版本: $VERSION
- 构建时间: $BUILD_TIME  
- Git提交: $GIT_COMMIT

## 目录结构
\`\`\`
release/
├── bin/                # 二进制文件目录
│   └── $APP_NAME       # 主程序
├── start.sh            # 启动脚本
├── stop.sh             # 停止脚本
├── config/             # 配置文件目录
│   ├── auth.yaml       # 默认认证配置
│   ├── auth-dev.yaml   # 开发环境配置
│   ├── auth-production.yaml # 生产环境配置
│   └── trpc_go.yaml    # TRPC框架配置
├── log/                # 日志目录
└── data/               # 数据目录（数据库文件等）
\`\`\`

## 使用方法

### 直接运行
\`\`\`bash
./bin/$APP_NAME -conf=./config/trpc_go.yaml
\`\`\`

### 使用启动脚本（推荐）
\`\`\`bash
# 启动服务
./start.sh

# 停止服务  
./stop.sh
\`\`\`

### 配置文件
根据部署环境选择相应的配置文件：
- 开发环境：使用 \`config/auth-dev.yaml\`
- 生产环境：使用 \`config/auth-production.yaml\`
- 默认配置：使用 \`config/auth.yaml\`

### 数据库初始化
首次部署时，需要手动初始化数据库，SQL脚本位于项目的sql目录：
\`\`\`bash
# SQLite（如果使用SQLite）
cd data && sqlite3 auth.db < ../../sql/schema.sql

# PostgreSQL（如果使用PostgreSQL）  
psql -d your_database -f ../sql/schema.sql
\`\`\`

### 环境变量
生产环境建议设置以下环境变量：
\`\`\`bash
export DB_HOST="your-db-host"
export DB_USER="your-db-user"
export DB_PASSWORD="your-secure-password"  
export DB_NAME="moox_auth_prod"
export JWT_SECRET_KEY="your-super-secure-jwt-key"
\`\`\`

### 日志
服务日志存储在 \`log/app.log\` 文件中。

## 注意事项
1. 确保端口没有被占用
2. 确保数据库配置正确
3. 生产环境请修改JWT密钥
4. 定期备份数据目录
EOF

# 显示构建结果
print_success "构建完成！"
echo ""
echo "构建结果："
echo "----------------------------------------"
echo "二进制文件: $BUILD_DIR/bin/$APP_NAME"
echo "配置目录:   $BUILD_DIR/config/"
echo "日志目录:   $BUILD_DIR/log/"
echo "启动脚本:   $BUILD_DIR/start.sh"
echo "停止脚本:   $BUILD_DIR/stop.sh"
echo "使用说明:   $BUILD_DIR/README.md"
echo ""
echo "文件列表："
ls -la "$BUILD_DIR/"
echo ""
print_info "进入构建目录: cd $BUILD_DIR"
print_info "启动服务: ./start.sh"
