#!/bin/bash
# 检查 RocksDB 安装状态

echo "================================"
echo "RocksDB 安装状态检查"
echo "================================"
echo ""

# 检查操作系统
echo "1. 操作系统信息："
echo "   $(uname -s) $(uname -m)"
echo ""

# 检查静态库
echo "2. 检查 RocksDB 静态库："
STATIC_LIB=""
if [ -f "/usr/local/lib/librocksdb.a" ]; then
    STATIC_LIB="/usr/local/lib/librocksdb.a"
    echo "   ✓ 找到: $STATIC_LIB"
    ls -lh "$STATIC_LIB"
elif [ -f "/usr/local/lib64/librocksdb.a" ]; then
    STATIC_LIB="/usr/local/lib64/librocksdb.a"
    echo "   ✓ 找到: $STATIC_LIB"
    ls -lh "$STATIC_LIB"
else
    echo "   ✗ 未找到 librocksdb.a"
    echo "   需要运行: make install-rocksdb"
    MISSING_STATIC=1
fi
echo ""

# 检查动态库
echo "3. 检查 RocksDB 动态库："
SHARED_LIB=""
if [ -f "/usr/local/lib/librocksdb.so" ]; then
    SHARED_LIB="/usr/local/lib/librocksdb.so"
    echo "   ✓ 找到: $SHARED_LIB"
    ls -lh "$SHARED_LIB"*
elif [ -f "/usr/local/lib64/librocksdb.so" ]; then
    SHARED_LIB="/usr/local/lib64/librocksdb.so"
    echo "   ✓ 找到: $SHARED_LIB"
    ls -lh "$SHARED_LIB"*
else
    echo "   ✗ 未找到 librocksdb.so"
fi
echo ""

# 检查头文件
echo "4. 检查 RocksDB 头文件："
if [ -d "/usr/local/include/rocksdb" ]; then
    echo "   ✓ 找到: /usr/local/include/rocksdb"
    echo "   头文件数量: $(find /usr/local/include/rocksdb -name '*.h' | wc -l)"
else
    echo "   ✗ 未找到 /usr/local/include/rocksdb"
    MISSING_HEADERS=1
fi
echo ""

# 检查依赖库
echo "5. 检查 RocksDB 依赖库："
DEPS=("z" "snappy" "lz4" "zstd" "bz2")
for dep in "${DEPS[@]}"; do
    if ldconfig -p 2>/dev/null | grep -q "lib${dep}.so"; then
        echo "   ✓ lib${dep} 已安装"
    else
        echo "   ✗ lib${dep} 未安装"
        MISSING_DEPS=1
    fi
done
echo ""

# 检查动态链接库配置
echo "6. 检查动态链接库配置："
if ldconfig -p 2>/dev/null | grep -q librocksdb; then
    echo "   ✓ librocksdb 在动态链接库缓存中"
    ldconfig -p 2>/dev/null | grep librocksdb | head -1
else
    echo "   ⚠ librocksdb 不在动态链接库缓存中（静态链接不需要）"
fi
echo ""

# 检查 pkg-config
echo "7. 检查 pkg-config 配置："
if pkg-config --exists rocksdb 2>/dev/null; then
    echo "   ✓ pkg-config 可以找到 rocksdb"
    echo "   版本: $(pkg-config --modversion rocksdb)"
    echo "   CFLAGS: $(pkg-config --cflags rocksdb)"
    echo "   LIBS: $(pkg-config --libs rocksdb)"
else
    echo "   ⚠ pkg-config 未配置 rocksdb（不影响静态链接）"
fi
echo ""

# 总结
echo "================================"
echo "总结"
echo "================================"
if [ -z "$MISSING_STATIC" ] && [ -z "$MISSING_HEADERS" ]; then
    echo "✓ RocksDB 静态库和头文件已正确安装"
    echo "✓ 可以进行静态链接编译"
    echo ""
    echo "运行以下命令开始构建："
    echo "  make build-linux VERSION=v1.0.0"
    exit 0
else
    echo "✗ RocksDB 安装不完整"
    echo ""
    if [ ! -z "$MISSING_STATIC" ]; then
        echo "  - 缺少静态库 (librocksdb.a)"
    fi
    if [ ! -z "$MISSING_HEADERS" ]; then
        echo "  - 缺少头文件"
    fi
    if [ ! -z "$MISSING_DEPS" ]; then
        echo "  - 缺少某些依赖库"
    fi
    echo ""
    echo "请运行以下命令安装 RocksDB："
    echo "  make install-rocksdb"
    exit 1
fi
