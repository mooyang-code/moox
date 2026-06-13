#!/bin/bash
# 从 GitHub main 分支编译 RocksDB 最新版本

set -e

echo "================================"
echo "从 main 分支编译 RocksDB"
echo "================================"
echo ""

# 检测操作系统
OS="$(uname -s)"
case "${OS}" in
    Linux*)     MACHINE=Linux;;
    Darwin*)
        echo "❌ macOS 用户请使用 Homebrew 安装: brew install rocksdb"
        exit 1
        ;;
    *)
        echo "❌ 不支持的操作系统: ${OS}"
        exit 1
        ;;
esac

# 安装编译依赖
echo "步骤 1: 安装编译依赖..."
if command -v yum &> /dev/null; then
    # CentOS/RHEL
    sudo yum install -y gcc-c++ make cmake git
    sudo yum install -y snappy snappy-devel
    sudo yum install -y zlib zlib-devel
    sudo yum install -y bzip2 bzip2-devel
    sudo yum install -y lz4-devel
    sudo yum install -y libzstd-devel
    sudo yum install -y gflags-devel
elif command -v apt-get &> /dev/null; then
    # Ubuntu/Debian
    sudo apt-get update
    sudo apt-get install -y build-essential cmake git
    sudo apt-get install -y libsnappy-dev
    sudo apt-get install -y zlib1g-dev
    sudo apt-get install -y libbz2-dev
    sudo apt-get install -y liblz4-dev
    sudo apt-get install -y libzstd-dev
    sudo apt-get install -y libgflags-dev
else
    echo "❌ 未知的 Linux 包管理器"
    exit 1
fi

# 克隆 RocksDB 主分支
echo ""
echo "步骤 2: 克隆 RocksDB 主分支..."
cd /tmp
if [ -d "rocksdb-main" ]; then
    echo "删除旧的源码目录..."
    rm -rf rocksdb-main
fi

git clone --depth 1 https://github.com/facebook/rocksdb.git rocksdb-main
cd rocksdb-main

# 显示当前 commit
echo ""
echo "当前 commit:"
git log -1 --oneline

# 清理之前的编译
echo ""
echo "步骤 3: 清理之前的编译..."
make clean || true

# 编译 RocksDB
echo ""
echo "步骤 4: 编译 RocksDB 静态库 (这可能需要几分钟)..."
EXTRA_CXXFLAGS="-fPIC" make static_lib -j$(nproc)

echo ""
echo "步骤 5: 编译 RocksDB 动态库..."
EXTRA_CXXFLAGS="-fPIC" make shared_lib -j$(nproc)

# 安装 RocksDB
echo ""
echo "步骤 6: 安装 RocksDB..."
sudo make install-static
sudo make install-shared

# 配置动态链接库
echo ""
echo "步骤 7: 配置动态链接库..."
if [ ! -f "/etc/ld.so.conf.d/rocksdb.conf" ]; then
    echo "创建 /etc/ld.so.conf.d/rocksdb.conf"
    echo "/usr/local/lib" | sudo tee /etc/ld.so.conf.d/rocksdb.conf > /dev/null
fi

echo "更新动态链接库缓存..."
sudo ldconfig

echo "验证动态链接库配置:"
if ldconfig -p | grep -q librocksdb; then
    ldconfig -p | grep librocksdb
    echo "✓ 动态链接库配置成功"
else
    echo "⚠ 警告: librocksdb 未在动态链接库缓存中找到"
fi

# 验证安装
echo ""
echo "步骤 8: 验证安装..."
if [ -f "/usr/local/lib/librocksdb.so" ] || [ -f "/usr/local/lib64/librocksdb.so" ]; then
    echo "✓ RocksDB 安装成功!"
    echo ""
    echo "库文件位置:"
    ls -lh /usr/local/lib*/librocksdb.* 2>/dev/null || true
    echo ""
    echo "头文件位置:"
    ls -d /usr/local/include/rocksdb 2>/dev/null || true
else
    echo "✗ 安装可能失败，请检查错误信息"
    exit 1
fi

# 验证关键 API
echo ""
echo "步骤 9: 验证关键 API..."
if grep -q "rocksdb_slice_t" /usr/local/include/rocksdb/c.h; then
    echo "✓ 找到 rocksdb_slice_t 定义"
else
    echo "⚠ 未找到 rocksdb_slice_t 定义"
fi

if grep -q "rocksdb_options_set_access_hint_on_compaction_start" /usr/local/include/rocksdb/c.h; then
    echo "✓ 找到 rocksdb_options_set_access_hint_on_compaction_start 定义"
else
    echo "⚠ 未找到 rocksdb_options_set_access_hint_on_compaction_start 定义"
fi

# 清理
echo ""
echo "步骤 10: 清理临时文件..."
cd /tmp
rm -rf rocksdb-main

echo ""
echo "================================"
echo "✓ 安装完成!"
echo "================================"
echo "RocksDB (main 分支) 已安装"
echo "现在可以编译 Go 项目了"
