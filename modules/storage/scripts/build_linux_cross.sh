#!/bin/bash
# Cross-build Linux amd64 from macOS without Docker.
#
# The script prepares missing Linux amd64 dependencies under ./.deps, then
# builds the Go binary with CGO enabled.

set -euo pipefail

APP_NAME="${APP_NAME:-xdata-storage}"
VERSION="${VERSION:-dev}"
BUILD_TIME="${BUILD_TIME:-$(date +"%Y-%m-%d_%H:%M:%S")}"
GIT_COMMIT="${GIT_COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}"
RELEASE_DIR="${RELEASE_DIR:-./release}"
CONFIG_DIR="${CONFIG_DIR:-./config}"
DEPS_ROOT="${DEPS_ROOT:-./.deps}"
LINUX_TARGET="${LINUX_TARGET:-x86_64-linux-gnu.2.35}"
LINUX_TARGET_ID="${LINUX_TARGET//[^A-Za-z0-9._-]/-}"
LINUX_TARGET_ID="${LINUX_TARGET_ID#x86_64-linux-gnu.}"
ROCKSDB_LINUX_PREFIX="${ROCKSDB_LINUX_PREFIX:-./.deps/linux-amd64-gnu-${LINUX_TARGET_ID}}"
PREBUILT_DEPS_ARCHIVE="${PREBUILT_DEPS_ARCHIVE:-./release/xdata-storage-linux-amd64-deps.tar.gz}"
mkdir -p "$DEPS_ROOT" "$ROCKSDB_LINUX_PREFIX"
DEPS_ROOT="$(cd "$DEPS_ROOT" && pwd)"
ROCKSDB_LINUX_PREFIX="$(cd "$ROCKSDB_LINUX_PREFIX" && pwd)"
if [ -f "$PREBUILT_DEPS_ARCHIVE" ]; then
    PREBUILT_DEPS_ARCHIVE="$(cd "$(dirname "$PREBUILT_DEPS_ARCHIVE")" && pwd)/$(basename "$PREBUILT_DEPS_ARCHIVE")"
fi
TOOLS_DIR="$DEPS_ROOT/tools"
SRC_DIR="$DEPS_ROOT/_src"
BUILD_DIR="$DEPS_ROOT/_build"
WRAPPER_DIR="$DEPS_ROOT/_toolchain"
GNU_RUNTIME_DIR="$DEPS_ROOT/linux-amd64-gnu-runtime-jammy"
GNU_RUNTIME_STDCXX_LIB_DIR="$GNU_RUNTIME_DIR/usr/lib/x86_64-linux-gnu"
GNU_RUNTIME_GCC_LIB_DIR="$GNU_RUNTIME_DIR/lib/x86_64-linux-gnu"
GNU_RUNTIME_GCC_DEV_LIB_DIR="$GNU_RUNTIME_DIR/usr/lib/gcc/x86_64-linux-gnu/12"
GNU_RUNTIME_CXX_INCLUDE_DIR="$GNU_RUNTIME_DIR/usr/include/c++/12"
GNU_RUNTIME_CXX_TARGET_INCLUDE_DIR="$GNU_RUNTIME_DIR/usr/include/x86_64-linux-gnu/c++/12"
ZIG_LIBC_INCLUDE_DIR=""
PATCHED_MODS_DIR="$DEPS_ROOT/_patched_mods"

ZIG_VERSION="${ZIG_VERSION:-0.14.1}"
CMAKE_VERSION="${CMAKE_VERSION:-3.29.8}"
ROCKSDB_REF="${ROCKSDB_REF:-main}"
SNAPPY_VERSION="${SNAPPY_VERSION:-1.2.2}"
LZ4_VERSION="${LZ4_VERSION:-1.10.0}"
ZSTD_VERSION="${ZSTD_VERSION:-1.5.7}"
ZLIB_VERSION="${ZLIB_VERSION:-1.3.1}"
BZIP2_VERSION="${BZIP2_VERSION:-1.0.8}"
UBUNTU_GCC_BUILD="${UBUNTU_GCC_BUILD:-12.3.0-1ubuntu1~22.04.3}"
GNU_RUNTIME_ID="ubuntu-22.04-gcc-${UBUNTU_GCC_BUILD}"

NCPU="$(sysctl -n hw.ncpu 2>/dev/null || echo 4)"

# Avoid leaking host build flags from the parent Makefile into third-party
# C/C++ builds.
unset EXTRA_LDFLAGS

log_step() {
    echo ""
    echo "$1"
    echo "------------------------------------"
}

die() {
    echo "❌ $*" >&2
    exit 1
}

download() {
    local url="$1"
    local output="$2"
    if [ -f "$output" ]; then
        echo "✓ 已存在: $output"
        return
    fi
    echo "下载: $url"
    curl -fL --retry 3 --retry-delay 2 -o "$output" "$url"
}

download_with_fallback() {
    local output="$1"
    shift
    if [ -f "$output" ]; then
        echo "✓ 已存在: $output"
        return
    fi
    local url
    for url in "$@"; do
        echo "下载: $url"
        if curl -fL --retry 3 --retry-delay 2 -o "$output.tmp" "$url"; then
            mv "$output.tmp" "$output"
            return
        fi
        rm -f "$output.tmp"
    done
    die "下载失败: $output"
}

extract_tar() {
    local archive="$1"
    local dest="$2"
    if [ -d "$dest" ]; then
        echo "✓ 已解压: $dest"
        return
    fi
    mkdir -p "$dest.tmp"
    tar -xf "$archive" -C "$dest.tmp" --strip-components=1
    mv "$dest.tmp" "$dest"
}

extract_deb() {
    local archive="$1"
    local dest="$2"
    mkdir -p "$dest"
    local tmp
    tmp="$(mktemp -d "$BUILD_DIR/deb.XXXXXX")"
    (
        cd "$tmp"
        ar -x "$archive"
        local data_archive
        data_archive="$(ls data.tar.* | head -1)"
        [ -n "$data_archive" ] || die "无法解包 Debian 包: $archive"
        tar -xf "$data_archive" -C "$dest"
    )
    rm -rf "$tmp"
}

need_linux_deps() {
    [ ! -f "$ROCKSDB_LINUX_PREFIX/include/rocksdb/c.h" ] && return 0
    [ ! -f "$ROCKSDB_LINUX_PREFIX/.rocksdb-ref" ] && return 0
    [ "$(cat "$ROCKSDB_LINUX_PREFIX/.rocksdb-ref")" != "$ROCKSDB_REF" ] && return 0
    [ ! -f "$ROCKSDB_LINUX_PREFIX/.linux-target" ] && return 0
    [ "$(cat "$ROCKSDB_LINUX_PREFIX/.linux-target")" != "$LINUX_TARGET" ] && return 0
    [ ! -f "$ROCKSDB_LINUX_PREFIX/.gnu-runtime" ] && return 0
    [ "$(cat "$ROCKSDB_LINUX_PREFIX/.gnu-runtime")" != "$GNU_RUNTIME_ID" ] && return 0
    [ ! -f "$ROCKSDB_LINUX_PREFIX/.cxx-stdlib" ] && return 0
    [ "$(cat "$ROCKSDB_LINUX_PREFIX/.cxx-stdlib")" != "gnu-libstdc++" ] && return 0
    ! grep -q "typedef struct rocksdb_livefile_t" "$ROCKSDB_LINUX_PREFIX/include/rocksdb/c.h" && return 0
    ! grep -q "typedef struct rocksdb_export_import_files_metadata_t" "$ROCKSDB_LINUX_PREFIX/include/rocksdb/c.h" && return 0
    ! grep -q "typedef struct rocksdb_slice_t" "$ROCKSDB_LINUX_PREFIX/include/rocksdb/c.h" && return 0
    ! grep -q "rocksdb_batched_multi_get_cf_slice" "$ROCKSDB_LINUX_PREFIX/include/rocksdb/c.h" && return 0
    ! grep -q "rocksdb_checkpoint_export_column_family" "$ROCKSDB_LINUX_PREFIX/include/rocksdb/c.h" && return 0
    ! grep -q "rocksdb_create_column_family_with_import" "$ROCKSDB_LINUX_PREFIX/include/rocksdb/c.h" && return 0
    ! grep -q "rocksdb_export_import_files_metadata_create" "$ROCKSDB_LINUX_PREFIX/include/rocksdb/c.h" && return 0
    ! grep -q "rocksdb_get_pinned_v2" "$ROCKSDB_LINUX_PREFIX/include/rocksdb/c.h" && return 0
    ! grep -q "rocksdb_livefile_create" "$ROCKSDB_LINUX_PREFIX/include/rocksdb/c.h" && return 0
    ! grep -q "rocksdb_livefiles_directory" "$ROCKSDB_LINUX_PREFIX/include/rocksdb/c.h" && return 0
    ! grep -q "rocksdb_options_set_skip_checking_sst_file_sizes_on_db_open" "$ROCKSDB_LINUX_PREFIX/include/rocksdb/c.h" && return 0
    ! grep -q "unsigned char (\*in_range)" "$ROCKSDB_LINUX_PREFIX/include/rocksdb/c.h" && return 0
    for lib in librocksdb.a libsnappy.a libz.a liblz4.a libzstd.a libbz2.a; do
        [ ! -f "$ROCKSDB_LINUX_PREFIX/lib/$lib" ] && return 0
    done
    return 1
}

need_prebuilt_deps() {
    [ ! -f "$ROCKSDB_LINUX_PREFIX/lib/librocksdb.a" ] && return 0
    [ ! -f "$ROCKSDB_LINUX_PREFIX/include/rocksdb/c.h" ] && return 0
    [ ! -f "$ROCKSDB_LINUX_PREFIX/.linux-target" ] && return 0
    [ "$(cat "$ROCKSDB_LINUX_PREFIX/.linux-target")" != "$LINUX_TARGET" ] && return 0
    [ ! -f "$ROCKSDB_LINUX_PREFIX/.gnu-runtime" ] && return 0
    [ "$(cat "$ROCKSDB_LINUX_PREFIX/.gnu-runtime")" != "$GNU_RUNTIME_ID" ] && return 0
    [ ! -f "$ROCKSDB_LINUX_PREFIX/.cxx-stdlib" ] && return 0
    [ ! -f "$GNU_RUNTIME_GCC_DEV_LIB_DIR/libstdc++.a" ] && return 0
    [ ! -f "$GNU_RUNTIME_GCC_LIB_DIR/libgcc_s.so.1" ] && return 0
    return 1
}

restore_prebuilt_deps() {
    if [ -f "$PREBUILT_DEPS_ARCHIVE" ] && need_prebuilt_deps; then
        log_step "检测到预编译 Linux 依赖包，直接恢复"
        tar -C "$DEPS_ROOT" -xzf "$PREBUILT_DEPS_ARCHIVE"
        echo "✓ 已恢复: $PREBUILT_DEPS_ARCHIVE"
    fi
}

ensure_zig() {
    if command -v zig >/dev/null 2>&1; then
        ZIG_BIN="$(command -v zig)"
        return
    fi

    mkdir -p "$TOOLS_DIR"
    local mac_arch
    mac_arch="$(uname -m)"
    case "$mac_arch" in
        arm64) zig_arch="aarch64" ;;
        x86_64) zig_arch="x86_64" ;;
        *) die "不支持的 macOS 架构: $mac_arch" ;;
    esac

    local zig_name="zig-${zig_arch}-macos-${ZIG_VERSION}"
    local zig_archive="$TOOLS_DIR/${zig_name}.tar.xz"
    local zig_dir="$TOOLS_DIR/$zig_name"
    download "https://ziglang.org/download/${ZIG_VERSION}/${zig_name}.tar.xz" "$zig_archive"
    extract_tar "$zig_archive" "$zig_dir"
    ZIG_BIN="$zig_dir/zig"
}

set_zig_libc_include_dir() {
    ZIG_LIBC_INCLUDE_DIR="$(cd "$(dirname "$ZIG_BIN")/lib/libc/include" && pwd)"
}

ensure_cmake() {
    if command -v cmake >/dev/null 2>&1; then
        CMAKE_BIN="$(command -v cmake)"
        return
    fi

    mkdir -p "$TOOLS_DIR"
    local cmake_name="cmake-${CMAKE_VERSION}-macos-universal"
    local cmake_archive="$TOOLS_DIR/${cmake_name}.tar.gz"
    local cmake_dir="$TOOLS_DIR/$cmake_name"
    download "https://github.com/Kitware/CMake/releases/download/v${CMAKE_VERSION}/${cmake_name}.tar.gz" "$cmake_archive"
    extract_tar "$cmake_archive" "$cmake_dir"
    CMAKE_BIN="$cmake_dir/CMake.app/Contents/bin/cmake"
}

ensure_gnu_runtime() {
    if [[ "$LINUX_TARGET" != x86_64-linux-gnu* ]]; then
        return
    fi
    if [ -f "$GNU_RUNTIME_STDCXX_LIB_DIR/libstdc++.so.6" ] &&
        [ -f "$GNU_RUNTIME_STDCXX_LIB_DIR/libstdc++.so" ] &&
        [ -f "$GNU_RUNTIME_GCC_DEV_LIB_DIR/libstdc++.a" ] &&
        [ -d "$GNU_RUNTIME_CXX_INCLUDE_DIR" ] &&
        [ -d "$GNU_RUNTIME_CXX_TARGET_INCLUDE_DIR" ] &&
        [ -f "$GNU_RUNTIME_GCC_LIB_DIR/libgcc_s.so.1" ] &&
        [ -f "$GNU_RUNTIME_GCC_LIB_DIR/libgcc_s.so" ]; then
        return
    fi

    log_step "准备 Ubuntu 22.04 GNU libstdc++ 运行库 $UBUNTU_GCC_BUILD"
    mkdir -p "$TOOLS_DIR/debian" "$GNU_RUNTIME_DIR" "$BUILD_DIR"
    local pkg base_url archive
    for pkg in "gcc-12-base" "libgcc-s1" "libstdc++6" "libstdc++-12-dev"; do
        if [ "$pkg" = "libstdc++-12-dev" ]; then
            base_url="https://archive.ubuntu.com/ubuntu/pool/universe/g/gcc-12"
        else
            base_url="https://archive.ubuntu.com/ubuntu/pool/main/g/gcc-12"
        fi
        archive="$TOOLS_DIR/debian/${pkg}_${UBUNTU_GCC_BUILD}_amd64.deb"
        download "$base_url/${pkg}_${UBUNTU_GCC_BUILD}_amd64.deb" "$archive"
        extract_deb "$archive" "$GNU_RUNTIME_DIR"
    done
    ln -sf libstdc++.so.6 "$GNU_RUNTIME_STDCXX_LIB_DIR/libstdc++.so"
    ln -sf libgcc_s.so.1 "$GNU_RUNTIME_GCC_LIB_DIR/libgcc_s.so"
}

ensure_stdlib_placeholder() {
    local placeholder="$ROCKSDB_LINUX_PREFIX/lib/libstdc++.a"
    if [ -f "$placeholder" ]; then
        return
    fi

    local empty_obj="$BUILD_DIR/empty-stdlib-placeholder.o"
    mkdir -p "$ROCKSDB_LINUX_PREFIX/lib" "$BUILD_DIR"
    "$ZIG_BIN" cc -target "$LINUX_TARGET" -x c -c /dev/null -o "$empty_obj"
    "$ZIG_BIN" ar rcs "$placeholder" "$empty_obj"
}

ensure_patched_duckdb_bindings() {
    local module="github.com/duckdb/duckdb-go-bindings/linux-amd64"
    local version="v0.1.12"
    local src
    src="$(go list -m -f '{{.Dir}}' "${module}@${version}")"
    [ -d "$src" ] || die "未找到 DuckDB linux-amd64 绑定模块: $module@$version"

    DUCKDB_PATCHED_DIR="$PATCHED_MODS_DIR/duckdb-go-bindings-linux-amd64-${version}"
    if [ ! -f "$DUCKDB_PATCHED_DIR/.patched-gnu-libstdcxx" ]; then
        rm -rf "$DUCKDB_PATCHED_DIR"
        mkdir -p "$PATCHED_MODS_DIR"
        cp -R "$src" "$DUCKDB_PATCHED_DIR"
        chmod -R u+w "$DUCKDB_PATCHED_DIR"
        perl -0pi -e 's/ -lstdc\+\+//g' "$DUCKDB_PATCHED_DIR/cgo_static.go"
        echo "$version" > "$DUCKDB_PATCHED_DIR/.patched-gnu-libstdcxx"
    fi

    BUILD_MODFILE="$BUILD_DIR/go.linux-cross.mod"
    cp go.mod "$BUILD_MODFILE"
    cp go.sum "$BUILD_DIR/go.linux-cross.sum"
    go mod edit -modfile="$BUILD_MODFILE" -replace="${module}=${DUCKDB_PATCHED_DIR}"
}

write_toolchain_wrappers() {
    mkdir -p "$WRAPPER_DIR"
    cat > "$WRAPPER_DIR/linux-cc" <<EOF
#!/bin/bash
args=()
skip_next=0
for arg in "\$@"; do
  if [ "\$skip_next" -eq 1 ]; then
    skip_next=0
    continue
  fi
  case "\$arg" in
    -mmacosx-version-min=*) ;;
    -isysroot|-arch) skip_next=1 ;;
    *) args+=("\$arg") ;;
  esac
done
exec "$ZIG_BIN" cc -target "$LINUX_TARGET" -nostdlib++ "\${args[@]}"
EOF
    cat > "$WRAPPER_DIR/linux-cxx" <<EOF
#!/bin/bash
args=()
skip_next=0
for arg in "\$@"; do
  if [ "\$skip_next" -eq 1 ]; then
    skip_next=0
    continue
  fi
  case "\$arg" in
    -mmacosx-version-min=*) ;;
    -isysroot|-arch) skip_next=1 ;;
    *) args+=("\$arg") ;;
  esac
done
exec "$ZIG_BIN" c++ -target "$LINUX_TARGET" -nostdlib++ -nostdinc++ -I"$GNU_RUNTIME_CXX_INCLUDE_DIR" -I"$GNU_RUNTIME_CXX_TARGET_INCLUDE_DIR" -isystem "$ZIG_LIBC_INCLUDE_DIR/generic-glibc" -isystem "$ZIG_LIBC_INCLUDE_DIR/x86_64-linux-gnu" -isystem "$ZIG_LIBC_INCLUDE_DIR/any-linux-any" "\${args[@]}"
EOF
    cat > "$WRAPPER_DIR/linux-ar" <<EOF
#!/bin/bash
exec "$ZIG_BIN" ar "\$@"
EOF
    cat > "$WRAPPER_DIR/linux-ranlib" <<EOF
#!/bin/bash
exec "$ZIG_BIN" ranlib "\$@"
EOF
    chmod +x "$WRAPPER_DIR"/linux-*

    export CC="$WRAPPER_DIR/linux-cc"
    export CXX="$WRAPPER_DIR/linux-cxx"
    export AR="$WRAPPER_DIR/linux-ar"
    export RANLIB="$WRAPPER_DIR/linux-ranlib"
}

build_zlib() {
    [ -f "$ROCKSDB_LINUX_PREFIX/lib/libz.a" ] && return
    log_step "准备 zlib $ZLIB_VERSION"
    local archive="$SRC_DIR/zlib-${ZLIB_VERSION}.tar.gz"
    local src="$SRC_DIR/zlib-${ZLIB_VERSION}"
    download_with_fallback "$archive" \
        "https://zlib.net/zlib-${ZLIB_VERSION}.tar.gz" \
        "https://zlib.net/fossils/zlib-${ZLIB_VERSION}.tar.gz"
    extract_tar "$archive" "$src"
    rm -rf "$BUILD_DIR/zlib"
    cp -R "$src" "$BUILD_DIR/zlib"
    (
        cd "$BUILD_DIR/zlib"
        CHOST="$LINUX_TARGET" CC="$CC" AR="$AR" RANLIB="$RANLIB" ./configure --static --prefix="$ROCKSDB_LINUX_PREFIX"
        make -j"$NCPU"
        make install
    )
}

build_bzip2() {
    [ -f "$ROCKSDB_LINUX_PREFIX/lib/libbz2.a" ] && return
    log_step "准备 bzip2 $BZIP2_VERSION"
    local archive="$SRC_DIR/bzip2-${BZIP2_VERSION}.tar.gz"
    local src="$SRC_DIR/bzip2-${BZIP2_VERSION}"
    download "https://sourceware.org/pub/bzip2/bzip2-${BZIP2_VERSION}.tar.gz" "$archive"
    extract_tar "$archive" "$src"
    rm -rf "$BUILD_DIR/bzip2"
    cp -R "$src" "$BUILD_DIR/bzip2"
    (
        cd "$BUILD_DIR/bzip2"
        make -j"$NCPU" CC="$CC" AR="$AR" RANLIB="$RANLIB" CFLAGS="-O2 -fPIC -D_FILE_OFFSET_BITS=64" libbz2.a
        mkdir -p "$ROCKSDB_LINUX_PREFIX/lib" "$ROCKSDB_LINUX_PREFIX/include"
        cp libbz2.a "$ROCKSDB_LINUX_PREFIX/lib/"
        cp bzlib.h "$ROCKSDB_LINUX_PREFIX/include/"
    )
}

build_lz4() {
    [ -f "$ROCKSDB_LINUX_PREFIX/lib/liblz4.a" ] && return
    log_step "准备 lz4 $LZ4_VERSION"
    local archive="$SRC_DIR/lz4-${LZ4_VERSION}.tar.gz"
    local src="$SRC_DIR/lz4-${LZ4_VERSION}"
    download "https://github.com/lz4/lz4/archive/refs/tags/v${LZ4_VERSION}.tar.gz" "$archive"
    extract_tar "$archive" "$src"
    (
        cd "$src"
        make -C lib clean || true
        make -C lib -j"$NCPU" CC="$CC" AR="$AR" RANLIB="$RANLIB" BUILD_SHARED=no PREFIX="$ROCKSDB_LINUX_PREFIX" install
    )
}

build_zstd() {
    [ -f "$ROCKSDB_LINUX_PREFIX/lib/libzstd.a" ] && return
    log_step "准备 zstd $ZSTD_VERSION"
    local archive="$SRC_DIR/zstd-${ZSTD_VERSION}.tar.gz"
    local src="$SRC_DIR/zstd-${ZSTD_VERSION}"
    download "https://github.com/facebook/zstd/archive/refs/tags/v${ZSTD_VERSION}.tar.gz" "$archive"
    extract_tar "$archive" "$src"
    (
        cd "$src"
        make clean || true
        make -j"$NCPU" CC="$CC" AR="$AR" RANLIB="$RANLIB" HAVE_ZLIB=0 HAVE_LZMA=0 HAVE_LZ4=0 BUILD_SHARED=no PREFIX="$ROCKSDB_LINUX_PREFIX" install
    )
}

build_snappy() {
    [ -f "$ROCKSDB_LINUX_PREFIX/lib/libsnappy.a" ] && return
    log_step "准备 snappy $SNAPPY_VERSION"
    local archive="$SRC_DIR/snappy-${SNAPPY_VERSION}.tar.gz"
    local src="$SRC_DIR/snappy-${SNAPPY_VERSION}"
    local build="$BUILD_DIR/snappy"
    download "https://github.com/google/snappy/archive/refs/tags/${SNAPPY_VERSION}.tar.gz" "$archive"
    extract_tar "$archive" "$src"
    rm -rf "$build"
    mkdir -p "$build"
    (
        cd "$build"
        "$CMAKE_BIN" "$PWD/../../_src/snappy-${SNAPPY_VERSION}" \
            -DCMAKE_SYSTEM_NAME=Linux \
            -DCMAKE_SYSTEM_PROCESSOR=x86_64 \
            -DCMAKE_C_COMPILER="$CC" \
            -DCMAKE_CXX_COMPILER="$CXX" \
            -DCMAKE_AR="$AR" \
            -DCMAKE_RANLIB="$RANLIB" \
            -DCMAKE_INSTALL_PREFIX="$ROCKSDB_LINUX_PREFIX" \
            -DCMAKE_BUILD_TYPE=Release \
            -DBUILD_SHARED_LIBS=OFF \
            -DSNAPPY_BUILD_TESTS=OFF \
            -DSNAPPY_BUILD_BENCHMARKS=OFF
        "$CMAKE_BIN" --build . --target install --parallel "$NCPU"
    )
}

patch_rocksdb_slice_api() {
    local src="$1"
    local header="$src/include/rocksdb/c.h"
    local impl="$src/db/c.cc"

    if ! grep -q "using ROCKSDB_NAMESPACE::ExportImportFilesMetaData;" "$impl"; then
        perl -0pi -e 's/(using ROCKSDB_NAMESPACE::ColumnFamilyOptions;\n)/$1using ROCKSDB_NAMESPACE::ExportImportFilesMetaData;\nusing ROCKSDB_NAMESPACE::ImportColumnFamilyOptions;\n/' "$impl"
    fi

    if ! grep -q "typedef struct rocksdb_livefile_t" "$header"; then
        perl -0pi -e 's/(typedef struct rocksdb_livefiles_t rocksdb_livefiles_t;\n)/typedef struct rocksdb_livefile_t rocksdb_livefile_t;\n$1/' "$header"
    fi

    if ! grep -q "typedef struct rocksdb_import_column_family_options_t" "$header"; then
        perl -0pi -e 's/(typedef struct rocksdb_column_family_metadata_t\n    rocksdb_column_family_metadata_t;\n)/$1typedef struct rocksdb_import_column_family_options_t\n    rocksdb_import_column_family_options_t;\ntypedef struct rocksdb_export_import_files_metadata_t\n    rocksdb_export_import_files_metadata_t;\n/' "$header"
    fi

    if ! grep -q "typedef struct rocksdb_slice_t" "$header"; then
        perl -0pi -e 's/(typedef struct rocksdb_wait_for_compact_options_t\n    rocksdb_wait_for_compact_options_t;\n)/$1\n\/\* rocksdb_slice_t: Optimized slice type for high-performance C API operations.\n \* This struct is ABI-compatible with rocksdb::Slice for zero-copy interop. \*\/\ntypedef struct rocksdb_slice_t {\n  const char* data;\n  size_t size;\n} rocksdb_slice_t;\n/' "$header"
    fi

    if ! grep -q "rocksdb_batched_multi_get_cf_slice" "$header"; then
        perl -0pi -e 's/(extern ROCKSDB_LIBRARY_API void rocksdb_batched_multi_get_cf\(\n    rocksdb_t\* db, const rocksdb_readoptions_t\* options,\n    rocksdb_column_family_handle_t\* column_family, size_t num_keys,\n    const char\* const\* keys_list, const size_t\* keys_list_sizes,\n    rocksdb_pinnableslice_t\*\* values, char\*\* errs, const bool sorted_input\);\n)/$1\nextern ROCKSDB_LIBRARY_API void rocksdb_batched_multi_get_cf_slice(\n    rocksdb_t* db, const rocksdb_readoptions_t* options,\n    rocksdb_column_family_handle_t* column_family, size_t num_keys,\n    const rocksdb_slice_t* keys_list, rocksdb_pinnableslice_t** values,\n    char** errs, const bool sorted_input);\n/' "$header"
    fi

    if ! grep -q "rocksdb_iter_key_slice" "$header"; then
        perl -0pi -e 's/(extern ROCKSDB_LIBRARY_API void rocksdb_iter_get_error\(\n    const rocksdb_iterator_t\*, char\*\* errptr\);\n)/$1\nextern ROCKSDB_LIBRARY_API rocksdb_slice_t\nrocksdb_iter_key_slice(const rocksdb_iterator_t* iter);\nextern ROCKSDB_LIBRARY_API rocksdb_slice_t\nrocksdb_iter_value_slice(const rocksdb_iterator_t* iter);\nextern ROCKSDB_LIBRARY_API rocksdb_slice_t\nrocksdb_iter_timestamp_slice(const rocksdb_iterator_t* iter);\n/' "$header"
    fi

    if ! grep -q "rocksdb_batched_multi_get_cf_slice" "$impl"; then
        perl -0pi -e 's/(unsigned char rocksdb_key_may_exist\(rocksdb_t\* db,)/void rocksdb_batched_multi_get_cf_slice(\n    rocksdb_t* db, const rocksdb_readoptions_t* options,\n    rocksdb_column_family_handle_t* column_family, size_t num_keys,\n    const rocksdb_slice_t* keys_list, rocksdb_pinnableslice_t** values,\n    char** errs, const bool sorted_input) {\n  PinnableSlice* value_slices = new PinnableSlice[num_keys];\n  Status* statuses = new Status[num_keys];\n  const Slice* key_slices = reinterpret_cast<const Slice*>(keys_list);\n\n  db->rep->MultiGet(options->rep, column_family->rep, num_keys, key_slices,\n                    value_slices, statuses, sorted_input);\n\n  for (size_t i = 0; i < num_keys; ++i) {\n    if (statuses[i].ok()) {\n      values[i] = new (rocksdb_pinnableslice_t);\n      values[i]->rep = std::move(value_slices[i]);\n      errs[i] = nullptr;\n    } else {\n      values[i] = nullptr;\n      if (!statuses[i].IsNotFound()) {\n        errs[i] = strdup(statuses[i].ToString().c_str());\n      } else {\n        errs[i] = nullptr;\n      }\n    }\n  }\n\n  delete[] value_slices;\n  delete[] statuses;\n}\n\n$1/' "$impl"
    fi

    if ! grep -q "rocksdb_iter_key_slice" "$impl"; then
        perl -0pi -e 's/(void rocksdb_iter_refresh\(const rocksdb_iterator_t\* iter, char\*\* errptr\) \{)/rocksdb_slice_t rocksdb_iter_key_slice(const rocksdb_iterator_t* iter) {\n  Slice s = iter->rep->key();\n  rocksdb_slice_t result;\n  result.data = s.data();\n  result.size = s.size();\n  return result;\n}\n\nrocksdb_slice_t rocksdb_iter_value_slice(const rocksdb_iterator_t* iter) {\n  Slice s = iter->rep->value();\n  rocksdb_slice_t result;\n  result.data = s.data();\n  result.size = s.size();\n  return result;\n}\n\nrocksdb_slice_t rocksdb_iter_timestamp_slice(const rocksdb_iterator_t* iter) {\n  Slice s = iter->rep->timestamp();\n  rocksdb_slice_t result;\n  result.data = s.data();\n  result.size = s.size();\n  return result;\n}\n\n$1/' "$impl"
    fi

    if ! grep -q "rocksdb_checkpoint_export_column_family" "$header"; then
        perl -0pi -e 's/(extern ROCKSDB_LIBRARY_API void rocksdb_checkpoint_create\(\n    rocksdb_checkpoint_t\* checkpoint, const char\* checkpoint_dir,\n    uint64_t log_size_for_flush, char\*\* errptr\);\n)/$1\nextern ROCKSDB_LIBRARY_API rocksdb_export_import_files_metadata_t*\nrocksdb_checkpoint_export_column_family(\n    rocksdb_checkpoint_t* checkpoint,\n    rocksdb_column_family_handle_t* column_family, const char* export_dir,\n    char** errptr);\n/' "$header"
    fi

    if ! grep -q "rocksdb_checkpoint_export_column_family" "$impl"; then
        perl -0pi -e 's/(void rocksdb_checkpoint_object_destroy\(rocksdb_checkpoint_t\* checkpoint\) \{)/rocksdb_export_import_files_metadata_t* rocksdb_checkpoint_export_column_family(\n    rocksdb_checkpoint_t* checkpoint,\n    rocksdb_column_family_handle_t* column_family, const char* export_dir,\n    char** errptr) {\n  ExportImportFilesMetaData* metadata = nullptr;\n  if (SaveError(errptr,\n                checkpoint->rep->ExportColumnFamily(\n                    column_family->rep, std::string(export_dir), &metadata))) {\n    return nullptr;\n  }\n  rocksdb_export_import_files_metadata_t* result =\n      new rocksdb_export_import_files_metadata_t;\n  result->rep = metadata;\n  return result;\n}\n\n$1/' "$impl"
    fi

    if ! grep -q "struct rocksdb_livefile_t" "$impl"; then
        perl -0pi -e 's/(struct rocksdb_livefiles_t \{\n  std::vector<LiveFileMetaData> rep;\n\};\n)/struct rocksdb_livefile_t {\n  LiveFileMetaData rep;\n};\n$1/' "$impl"
    fi

    if ! grep -q "struct rocksdb_export_import_files_metadata_t" "$impl"; then
        perl -0pi -e 's/(struct rocksdb_column_family_metadata_t \{\n  ColumnFamilyMetaData rep;\n\};\n)/$1struct rocksdb_export_import_files_metadata_t {\n  ExportImportFilesMetaData* rep;\n};\nstruct rocksdb_import_column_family_options_t {\n  ImportColumnFamilyOptions rep;\n};\n/' "$impl"
    fi

    if ! grep -q "rocksdb_create_column_family_with_import" "$header"; then
        perl -0pi -e 's/(extern ROCKSDB_LIBRARY_API void rocksdb_create_column_families_destroy\(\n    rocksdb_column_family_handle_t\*\* list\);\n)/$1\nextern ROCKSDB_LIBRARY_API rocksdb_column_family_handle_t*\nrocksdb_create_column_family_with_import(\n    rocksdb_t* db, rocksdb_options_t* column_family_options,\n    const char* column_family_name,\n    rocksdb_import_column_family_options_t* import_options,\n    rocksdb_export_import_files_metadata_t* metadata, char** errptr);\n/' "$header"
    fi

    if ! grep -q "rocksdb_import_column_family_options_create" "$header"; then
        perl -0pi -e 's/(extern ROCKSDB_LIBRARY_API void rocksdb_drop_column_family\(\n    rocksdb_t\* db, rocksdb_column_family_handle_t\* handle, char\*\* errptr\);\n)/$1\nextern ROCKSDB_LIBRARY_API rocksdb_import_column_family_options_t*\nrocksdb_import_column_family_options_create(void);\n\nextern ROCKSDB_LIBRARY_API void\nrocksdb_import_column_family_options_set_move_files(\n    rocksdb_import_column_family_options_t*, unsigned char);\n\nextern ROCKSDB_LIBRARY_API void rocksdb_import_column_family_options_destroy(\n    rocksdb_import_column_family_options_t*);\n/' "$header"
    fi

    if ! grep -q "rocksdb_export_import_files_metadata_create" "$header"; then
        perl -0pi -e 's/(extern ROCKSDB_LIBRARY_API rocksdb_column_family_metadata_t\*\nrocksdb_get_column_family_metadata_cf\()/extern ROCKSDB_LIBRARY_API rocksdb_export_import_files_metadata_t*\nrocksdb_export_import_files_metadata_create(void);\n\nextern ROCKSDB_LIBRARY_API char*\nrocksdb_export_import_files_metadata_get_db_comparator_name(\n    rocksdb_export_import_files_metadata_t*);\n\nextern ROCKSDB_LIBRARY_API void\nrocksdb_export_import_files_metadata_set_db_comparator_name(\n    rocksdb_export_import_files_metadata_t*, const char*);\n\nextern ROCKSDB_LIBRARY_API rocksdb_livefiles_t*\nrocksdb_export_import_files_metadata_get_files(\n    rocksdb_export_import_files_metadata_t*);\n\nextern ROCKSDB_LIBRARY_API void rocksdb_export_import_files_metadata_set_files(\n    rocksdb_export_import_files_metadata_t*, rocksdb_livefiles_t*);\n\nextern ROCKSDB_LIBRARY_API void rocksdb_export_import_files_metadata_destroy(\n    rocksdb_export_import_files_metadata_t*);\n\n$1/' "$header"
    fi

    if ! grep -q "rocksdb_create_column_family_with_import" "$impl"; then
        perl -0pi -e 's/(void rocksdb_create_column_families_destroy\(\n    rocksdb_column_family_handle_t\*\* list\) \{[\s\S]*?\n\}\n\n)/$1rocksdb_column_family_handle_t* rocksdb_create_column_family_with_import(\n    rocksdb_t* db, rocksdb_options_t* column_family_options,\n    const char* column_family_name,\n    rocksdb_import_column_family_options_t* import_options,\n    rocksdb_export_import_files_metadata_t* export_import_files_metadata,\n    char** errptr) {\n  rocksdb_column_family_handle_t* handle = new rocksdb_column_family_handle_t;\n  handle->rep = nullptr;\n  if (SaveError(errptr,\n                db->rep->CreateColumnFamilyWithImport(\n                    ColumnFamilyOptions(column_family_options->rep),\n                    std::string(column_family_name), import_options->rep,\n                    *(export_import_files_metadata->rep), &(handle->rep)))) {\n    delete handle;\n    return nullptr;\n  }\n  handle->immortal = false;\n  return handle;\n}\n\n/' "$impl"
    fi

    if ! grep -q "rocksdb_import_column_family_options_create" "$impl"; then
        perl -0pi -e 's/(void rocksdb_drop_column_family\(rocksdb_t\* db,\n                                 rocksdb_column_family_handle_t\* handle,\n                                 char\*\* errptr\) \{[\s\S]*?\n\}\n\n)/$1rocksdb_import_column_family_options_t*\nrocksdb_import_column_family_options_create() {\n  return new rocksdb_import_column_family_options_t;\n}\n\nvoid rocksdb_import_column_family_options_set_move_files(\n    rocksdb_import_column_family_options_t* opt, unsigned char v) {\n  opt->rep.move_files = v;\n}\n\nvoid rocksdb_import_column_family_options_destroy(\n    rocksdb_import_column_family_options_t* opt) {\n  delete opt;\n}\n\n/' "$impl"
    fi

    if ! grep -q "rocksdb_export_import_files_metadata_create" "$impl"; then
        perl -0pi -e 's@(\/\* Transactions \*\/)@rocksdb_export_import_files_metadata_t*\nrocksdb_export_import_files_metadata_create() {\n  auto metadata = new rocksdb_export_import_files_metadata_t;\n  metadata->rep = new ExportImportFilesMetaData;\n  return metadata;\n}\n\nchar* rocksdb_export_import_files_metadata_get_db_comparator_name(\n    rocksdb_export_import_files_metadata_t* metadata) {\n  return strdup(metadata->rep->db_comparator_name.c_str());\n}\n\nvoid rocksdb_export_import_files_metadata_set_db_comparator_name(\n    rocksdb_export_import_files_metadata_t* metadata, const char* name) {\n  metadata->rep->db_comparator_name = std::string(name);\n}\n\nrocksdb_livefiles_t* rocksdb_export_import_files_metadata_get_files(\n    rocksdb_export_import_files_metadata_t* export_import_metadata) {\n  auto files = new rocksdb_livefiles_t;\n  files->rep = std::vector(export_import_metadata->rep->files);\n  return files;\n}\n\nvoid rocksdb_export_import_files_metadata_set_files(\n    rocksdb_export_import_files_metadata_t* metadata,\n    rocksdb_livefiles_t* files) {\n  metadata->rep->files = std::move(files->rep);\n  delete files;\n}\n\nvoid rocksdb_export_import_files_metadata_destroy(\n    rocksdb_export_import_files_metadata_t* metadata) {\n  delete metadata->rep;\n  delete metadata;\n}\n\n$1@' "$impl"
    fi

    if ! grep -q "rocksdb_get_pinned_v2" "$header"; then
        perl -0pi -e 's@(extern ROCKSDB_LIBRARY_API uint64_t\nrocksdb_wait_for_compact_options_get_timeout\(\n    rocksdb_wait_for_compact_options_t\* opt\);\n)@$1\n/* High-performance zero-copy Get variants. */\ntypedef struct rocksdb_pinnable_handle_t rocksdb_pinnable_handle_t;\n\nextern ROCKSDB_LIBRARY_API rocksdb_pinnable_handle_t* rocksdb_get_pinned_v2(\n    rocksdb_t* db, const rocksdb_readoptions_t* options, const char* key,\n    size_t keylen, char** errptr);\n\nextern ROCKSDB_LIBRARY_API rocksdb_pinnable_handle_t* rocksdb_get_pinned_cf_v2(\n    rocksdb_t* db, const rocksdb_readoptions_t* options,\n    rocksdb_column_family_handle_t* column_family, const char* key,\n    size_t keylen, char** errptr);\n\nextern ROCKSDB_LIBRARY_API const char* rocksdb_pinnable_handle_get_value(\n    const rocksdb_pinnable_handle_t* handle, size_t* vallen);\n\nextern ROCKSDB_LIBRARY_API void rocksdb_pinnable_handle_destroy(\n    rocksdb_pinnable_handle_t* handle);\n@' "$header"
    fi

    if ! grep -q "rocksdb_get_pinned_v2" "$impl"; then
        perl -0pi -e 's@(unsigned char rocksdb_get_into_buffer\(rocksdb_t\* db,)@struct rocksdb_pinnable_handle_t {\n  PinnableSlice rep;\n};\n\nrocksdb_pinnable_handle_t* rocksdb_get_pinned_v2(\n    rocksdb_t* db, const rocksdb_readoptions_t* options, const char* key,\n    size_t keylen, char** errptr) {\n  rocksdb_pinnable_handle_t* handle = new rocksdb_pinnable_handle_t;\n  Status s = db->rep->Get(options->rep, db->rep->DefaultColumnFamily(),\n                          Slice(key, keylen), &handle->rep);\n  if (!s.ok()) {\n    delete handle;\n    if (!s.IsNotFound()) {\n      SaveError(errptr, s);\n    }\n    return nullptr;\n  }\n  return handle;\n}\n\nrocksdb_pinnable_handle_t* rocksdb_get_pinned_cf_v2(\n    rocksdb_t* db, const rocksdb_readoptions_t* options,\n    rocksdb_column_family_handle_t* column_family, const char* key,\n    size_t keylen, char** errptr) {\n  rocksdb_pinnable_handle_t* handle = new rocksdb_pinnable_handle_t;\n  Status s = db->rep->Get(options->rep, column_family->rep, Slice(key, keylen),\n                          &handle->rep);\n  if (!s.ok()) {\n    delete handle;\n    if (!s.IsNotFound()) {\n      SaveError(errptr, s);\n    }\n    return nullptr;\n  }\n  return handle;\n}\n\nconst char* rocksdb_pinnable_handle_get_value(\n    const rocksdb_pinnable_handle_t* handle, size_t* vallen) {\n  if (!handle) {\n    *vallen = 0;\n    return nullptr;\n  }\n  *vallen = handle->rep.size();\n  return handle->rep.data();\n}\n\nvoid rocksdb_pinnable_handle_destroy(rocksdb_pinnable_handle_t* handle) {\n  delete handle;\n}\n\n$1@' "$impl"
    fi

    if ! grep -q "rocksdb_livefile_create" "$header"; then
        perl -0pi -e 's/(extern ROCKSDB_LIBRARY_API void rocksdb_livefiles_destroy\(\n    const rocksdb_livefiles_t\*\);\n)/extern ROCKSDB_LIBRARY_API rocksdb_livefiles_t* rocksdb_livefiles_create(void);\n$1\nextern ROCKSDB_LIBRARY_API uint64_t\nrocksdb_livefiles_smallest_seqno(const rocksdb_livefiles_t*, int index);\nextern ROCKSDB_LIBRARY_API uint64_t\nrocksdb_livefiles_largest_seqno(const rocksdb_livefiles_t*, int index);\n\nextern ROCKSDB_LIBRARY_API rocksdb_livefile_t* rocksdb_livefile_create(void);\nextern ROCKSDB_LIBRARY_API void rocksdb_livefile_set_column_family_name(\n    rocksdb_livefile_t*, const char*);\nextern ROCKSDB_LIBRARY_API void rocksdb_livefile_set_level(rocksdb_livefile_t*,\n                                                           int);\nextern ROCKSDB_LIBRARY_API void rocksdb_livefile_set_name(rocksdb_livefile_t*,\n                                                          const char*);\nextern ROCKSDB_LIBRARY_API void rocksdb_livefile_set_directory(\n    rocksdb_livefile_t*, const char*);\nextern ROCKSDB_LIBRARY_API void rocksdb_livefile_set_size(rocksdb_livefile_t*,\n                                                          size_t);\nextern ROCKSDB_LIBRARY_API void rocksdb_livefile_set_smallest_key(\n    rocksdb_livefile_t*, const char*, size_t);\nextern ROCKSDB_LIBRARY_API void rocksdb_livefile_set_largest_key(\n    rocksdb_livefile_t*, const char*, size_t);\nextern ROCKSDB_LIBRARY_API void rocksdb_livefile_set_smallest_seqno(\n    rocksdb_livefile_t*, uint64_t);\nextern ROCKSDB_LIBRARY_API void rocksdb_livefile_set_largest_seqno(\n    rocksdb_livefile_t*, uint64_t);\nextern ROCKSDB_LIBRARY_API void rocksdb_livefile_set_num_entries(\n    rocksdb_livefile_t*, uint64_t);\nextern ROCKSDB_LIBRARY_API void rocksdb_livefile_set_num_deletions(\n    rocksdb_livefile_t*, uint64_t);\nextern ROCKSDB_LIBRARY_API void rocksdb_livefile_destroy(rocksdb_livefile_t*);\n\nextern ROCKSDB_LIBRARY_API void rocksdb_livefiles_add(rocksdb_livefiles_t*,\n                                                      rocksdb_livefile_t*);\n/' "$header"
    fi

    if ! grep -q "rocksdb_livefiles_directory" "$header"; then
        perl -0pi -e 's/(extern ROCKSDB_LIBRARY_API const char\* rocksdb_livefiles_name\(\n    const rocksdb_livefiles_t\*, int index\);\n)/$1extern ROCKSDB_LIBRARY_API const char* rocksdb_livefiles_directory(\n    const rocksdb_livefiles_t*, int index);\n/' "$header"
    fi

    if ! grep -q "rocksdb_livefile_create" "$impl"; then
        perl -0pi -e 's/(void rocksdb_livefiles_destroy\(const rocksdb_livefiles_t\* lf\) \{ delete lf; \}\n\n)/rocksdb_livefiles_t* rocksdb_livefiles_create() {\n  return new rocksdb_livefiles_t;\n}\n\n$1uint64_t rocksdb_livefiles_smallest_seqno(const rocksdb_livefiles_t* lf,\n                                          int index) {\n  return lf->rep[index].smallest_seqno;\n}\n\nuint64_t rocksdb_livefiles_largest_seqno(const rocksdb_livefiles_t* lf,\n                                         int index) {\n  return lf->rep[index].largest_seqno;\n}\n\nrocksdb_livefile_t* rocksdb_livefile_create() { return new rocksdb_livefile_t; }\n\nvoid rocksdb_livefile_set_column_family_name(rocksdb_livefile_t* lf,\n                                             const char* column_family_name) {\n  lf->rep.column_family_name = std::string(column_family_name);\n}\n\nvoid rocksdb_livefile_set_level(rocksdb_livefile_t* lf, int level) {\n  lf->rep.level = level;\n}\n\nvoid rocksdb_livefile_set_name(rocksdb_livefile_t* lf, const char* name) {\n  lf->rep.name = std::string(name);\n}\n\nvoid rocksdb_livefile_set_directory(rocksdb_livefile_t* lf,\n                                    const char* directory) {\n  lf->rep.directory = std::string(directory);\n  lf->rep.db_path = std::string(directory);\n}\n\nvoid rocksdb_livefile_set_size(rocksdb_livefile_t* lf, size_t size) {\n  lf->rep.size = size;\n}\n\nvoid rocksdb_livefile_set_smallest_key(rocksdb_livefile_t* lf,\n                                       const char* smallest_key,\n                                       size_t smallest_key_len) {\n  lf->rep.smallestkey = std::string(smallest_key, smallest_key_len);\n}\n\nvoid rocksdb_livefile_set_largest_key(rocksdb_livefile_t* lf,\n                                      const char* largest_key,\n                                      size_t largest_key_len) {\n  lf->rep.largestkey = std::string(largest_key, largest_key_len);\n}\n\nvoid rocksdb_livefile_set_smallest_seqno(rocksdb_livefile_t* lf,\n                                         uint64_t smallest_seqno) {\n  lf->rep.smallest_seqno = smallest_seqno;\n}\n\nvoid rocksdb_livefile_set_largest_seqno(rocksdb_livefile_t* lf,\n                                        uint64_t largest_seqno) {\n  lf->rep.largest_seqno = largest_seqno;\n}\n\nvoid rocksdb_livefile_set_num_entries(rocksdb_livefile_t* lf,\n                                      uint64_t num_entries) {\n  lf->rep.num_entries = num_entries;\n}\n\nvoid rocksdb_livefile_set_num_deletions(rocksdb_livefile_t* lf,\n                                        uint64_t num_deletions) {\n  lf->rep.num_deletions = num_deletions;\n}\n\nvoid rocksdb_livefile_destroy(rocksdb_livefile_t* lf) { delete lf; }\n\nvoid rocksdb_livefiles_add(rocksdb_livefiles_t* lf,\n                           rocksdb_livefile_t* livefile) {\n  lf->rep.push_back(std::move(livefile->rep));\n  delete livefile;\n}\n\n/' "$impl"
    fi

    if ! grep -q "rocksdb_livefiles_directory" "$impl"; then
        perl -0pi -e 's/(const char\* rocksdb_livefiles_name\(const rocksdb_livefiles_t\* lf, int index\) \{\n  return lf->rep\[index\].name.c_str\(\);\n\}\n\n)/$1const char* rocksdb_livefiles_directory(const rocksdb_livefiles_t* lf,\n                                        int index) {\n  if (lf->rep[index].directory.empty()) {\n    return lf->rep[index].db_path.c_str();\n  }\n  return lf->rep[index].directory.c_str();\n}\n\n/' "$impl"
    fi

    if ! grep -q "rocksdb_options_set_skip_checking_sst_file_sizes_on_db_open" "$header"; then
        perl -0pi -e 's/(extern ROCKSDB_LIBRARY_API unsigned char\nrocksdb_options_get_skip_stats_update_on_db_open\(rocksdb_options_t\* opt\);\n)/$1extern ROCKSDB_LIBRARY_API void\nrocksdb_options_set_skip_checking_sst_file_sizes_on_db_open(\n    rocksdb_options_t* opt, unsigned char val);\nextern ROCKSDB_LIBRARY_API unsigned char\nrocksdb_options_get_skip_checking_sst_file_sizes_on_db_open(\n    rocksdb_options_t* opt);\n/' "$header"
    fi

    if ! grep -q "rocksdb_options_set_skip_checking_sst_file_sizes_on_db_open" "$impl"; then
        perl -0pi -e 's/(unsigned char rocksdb_options_get_skip_stats_update_on_db_open\(\n    rocksdb_options_t\* opt\) \{\n  return opt->rep.skip_stats_update_on_db_open;\n\}\n\n)/$1void rocksdb_options_set_skip_checking_sst_file_sizes_on_db_open(\n    rocksdb_options_t* opt, unsigned char val) {\n  (void)opt;\n  (void)val;\n}\n\nunsigned char rocksdb_options_get_skip_checking_sst_file_sizes_on_db_open(\n    rocksdb_options_t* opt) {\n  (void)opt;\n  return 0;\n}\n\n/' "$impl"
    fi

    if ! grep -q "unsigned char (\\*in_range)" "$header"; then
        perl -0pi -e 's/(unsigned char \(\*in_domain\)\(void\*, const char\* key,\n                                                         size_t length\),\n)(                              const char\* \(\*name\)\(void\*\)\);)/$1                              unsigned char (*in_range)(void*, const char* key,\n                                                        size_t length),\n$2/' "$header"
    fi

    if ! grep -q "unsigned char (\\*in_range_)" "$impl"; then
        perl -0pi -e 's/(  unsigned char \(\*in_domain_\)\(void\*, const char\* key, size_t length\);\n)/$1  unsigned char (*in_range_)(void*, const char* key, size_t length);\n/' "$impl"
        perl -0pi -e 's/(  bool InDomain\(const Slice& src\) const override \{\n    return \(\*in_domain_\)\(state_, src.data\(\), src.size\(\)\);\n  \}\n)/$1\n  bool InRange(const Slice& dst) const override {\n    return (*in_range_)(state_, dst.data(), dst.size());\n  }\n/' "$impl"
        perl -0pi -e 's/(    unsigned char \(\*in_domain\)\(void\*, const char\* key, size_t length\),\n)(    const char\* \(\*name\)\(void\*\)\) \{)/$1    unsigned char (*in_range)(void*, const char* key, size_t length),\n$2/' "$impl"
        perl -0pi -e 's/(  result->in_domain_ = in_domain;\n)/$1  result->in_range_ = in_range;\n/' "$impl"
    fi
    perl -0pi -e 's/bool InRange\(const Slice& dst\) const override/bool InRange(const Slice& dst) const/' "$impl"
}

build_rocksdb() {
    if [ -f "$ROCKSDB_LINUX_PREFIX/lib/librocksdb.a" ] &&
        [ -f "$ROCKSDB_LINUX_PREFIX/include/rocksdb/c.h" ] &&
        [ -f "$ROCKSDB_LINUX_PREFIX/.rocksdb-ref" ] &&
        [ "$(cat "$ROCKSDB_LINUX_PREFIX/.rocksdb-ref")" = "$ROCKSDB_REF" ] &&
        grep -q "typedef struct rocksdb_slice_t" "$ROCKSDB_LINUX_PREFIX/include/rocksdb/c.h" &&
        grep -q "rocksdb_batched_multi_get_cf_slice" "$ROCKSDB_LINUX_PREFIX/include/rocksdb/c.h" &&
        grep -q "rocksdb_checkpoint_export_column_family" "$ROCKSDB_LINUX_PREFIX/include/rocksdb/c.h" &&
        grep -q "rocksdb_create_column_family_with_import" "$ROCKSDB_LINUX_PREFIX/include/rocksdb/c.h" &&
        grep -q "rocksdb_export_import_files_metadata_create" "$ROCKSDB_LINUX_PREFIX/include/rocksdb/c.h" &&
        grep -q "rocksdb_get_pinned_v2" "$ROCKSDB_LINUX_PREFIX/include/rocksdb/c.h" &&
        grep -q "rocksdb_livefile_create" "$ROCKSDB_LINUX_PREFIX/include/rocksdb/c.h" &&
        grep -q "rocksdb_livefiles_directory" "$ROCKSDB_LINUX_PREFIX/include/rocksdb/c.h" &&
        grep -q "rocksdb_options_set_skip_checking_sst_file_sizes_on_db_open" "$ROCKSDB_LINUX_PREFIX/include/rocksdb/c.h" &&
        grep -q "unsigned char (\*in_range)" "$ROCKSDB_LINUX_PREFIX/include/rocksdb/c.h"; then
        return
    fi
    log_step "准备 RocksDB $ROCKSDB_REF"
    local archive="$SRC_DIR/rocksdb-${ROCKSDB_REF}.tar.gz"
    local src="$SRC_DIR/rocksdb-${ROCKSDB_REF}"
    local url
    if [ "$ROCKSDB_REF" = "main" ]; then
        url="https://github.com/facebook/rocksdb/archive/refs/heads/main.tar.gz"
    else
        url="https://github.com/facebook/rocksdb/archive/refs/tags/${ROCKSDB_REF}.tar.gz"
    fi
    download "$url" "$archive"
    extract_tar "$archive" "$src"
    rm -rf "$ROCKSDB_LINUX_PREFIX/include/rocksdb" "$ROCKSDB_LINUX_PREFIX/lib/librocksdb.a"
    patch_rocksdb_slice_api "$src"
    (
        cd "$src"
        TARGET_OS=Linux TARGET_ARCHITECTURE=x86_64 \
            CC="$CC" CXX="$CXX" AR="$AR" RANLIB="$RANLIB" \
            make clean || true
        TARGET_OS=Linux TARGET_ARCHITECTURE=x86_64 \
            CC="$CC" CXX="$CXX" AR="$AR" RANLIB="$RANLIB" \
            CFLAGS="-I$ROCKSDB_LINUX_PREFIX/include -fPIC" \
            CXXFLAGS="-I$ROCKSDB_LINUX_PREFIX/include -fPIC" \
            LDFLAGS="-L$ROCKSDB_LINUX_PREFIX/lib" \
            PORTABLE=1 USE_RTTI=1 ROCKSDB_CXX_STANDARD="c++20" EXTRA_CXXFLAGS="-fPIC -Wno-error=unused-parameter" \
            make -j"$NCPU" static_lib
        mkdir -p "$ROCKSDB_LINUX_PREFIX/lib" "$ROCKSDB_LINUX_PREFIX/include"
        cp librocksdb.a "$ROCKSDB_LINUX_PREFIX/lib/"
        cp -R include/rocksdb "$ROCKSDB_LINUX_PREFIX/include/"
        echo "$ROCKSDB_REF" > "$ROCKSDB_LINUX_PREFIX/.rocksdb-ref"
        echo "$LINUX_TARGET" > "$ROCKSDB_LINUX_PREFIX/.linux-target"
        echo "$GNU_RUNTIME_ID" > "$ROCKSDB_LINUX_PREFIX/.gnu-runtime"
    )
}

prepare_linux_deps() {
    mkdir -p "$TOOLS_DIR" "$SRC_DIR" "$BUILD_DIR" "$ROCKSDB_LINUX_PREFIX/lib" "$ROCKSDB_LINUX_PREFIX/include"
    ensure_zig
    set_zig_libc_include_dir
    ensure_cmake
    ensure_gnu_runtime
    write_toolchain_wrappers
    ensure_stdlib_placeholder
    ensure_patched_duckdb_bindings
    if [ ! -f "$ROCKSDB_LINUX_PREFIX/.cxx-stdlib" ] ||
        [ "$(cat "$ROCKSDB_LINUX_PREFIX/.cxx-stdlib" 2>/dev/null || true)" != "gnu-libstdc++" ] ||
        [ "$(cat "$ROCKSDB_LINUX_PREFIX/.gnu-runtime" 2>/dev/null || true)" != "$GNU_RUNTIME_ID" ]; then
        rm -f "$ROCKSDB_LINUX_PREFIX/lib/libsnappy.a" "$ROCKSDB_LINUX_PREFIX/lib/librocksdb.a"
    fi
    echo "✓ Zig: $ZIG_BIN"
    echo "✓ CMake: $CMAKE_BIN"
echo "✓ Linux 依赖前缀: $ROCKSDB_LINUX_PREFIX"
echo "✓ Linux C/C++ ABI: $LINUX_TARGET"
    build_zlib
    build_bzip2
    build_lz4
    build_zstd
    build_snappy
    build_rocksdb
    echo "gnu-libstdc++" > "$ROCKSDB_LINUX_PREFIX/.cxx-stdlib"
    echo "$LINUX_TARGET" > "$ROCKSDB_LINUX_PREFIX/.linux-target"
    echo "$GNU_RUNTIME_ID" > "$ROCKSDB_LINUX_PREFIX/.gnu-runtime"
}

create_archives() {
    log_step "步骤 4: 生成发布归档"
    mkdir -p "$RELEASE_DIR"
    if command -v xattr >/dev/null 2>&1; then
        xattr -rc "$RELEASE_DIR/linux" "$ROCKSDB_LINUX_PREFIX" "$GNU_RUNTIME_DIR" 2>/dev/null || true
    fi
    COPYFILE_DISABLE=1 tar --no-xattrs -C "$RELEASE_DIR" -czf "$RELEASE_DIR/xdata-storage-linux-amd64.tar.gz" linux
    echo "✓ 运行包: $RELEASE_DIR/xdata-storage-linux-amd64.tar.gz"

    local deps_parent deps_name runtime_name
    deps_parent="$(cd "$(dirname "$ROCKSDB_LINUX_PREFIX")" && pwd)"
    deps_name="$(basename "$ROCKSDB_LINUX_PREFIX")"
    runtime_name="$(basename "$GNU_RUNTIME_DIR")"
    if [ "$deps_parent" = "$DEPS_ROOT" ]; then
        COPYFILE_DISABLE=1 tar --no-xattrs -C "$DEPS_ROOT" -czf "$PREBUILT_DEPS_ARCHIVE" "$deps_name" "$runtime_name"
        echo "✓ 依赖包: $PREBUILT_DEPS_ARCHIVE"
    else
        echo "⚠️  跳过依赖归档: ROCKSDB_LINUX_PREFIX 不在 DEPS_ROOT 下"
    fi
}

echo "================================"
echo "macOS -> Linux amd64 CGO 交叉构建"
echo "================================"
echo ""

[ "$(uname -s)" = "Darwin" ] || die "build-linux-cross 只用于 macOS 主机；Linux 主机请使用 make build-linux"

restore_prebuilt_deps

if need_linux_deps; then
    log_step "检测到 Linux 静态依赖缺失，开始自动准备"
    prepare_linux_deps
else
    ensure_zig
    set_zig_libc_include_dir
    ensure_gnu_runtime
    write_toolchain_wrappers
    ensure_stdlib_placeholder
    ensure_patched_duckdb_bindings
fi

[ -f "$ROCKSDB_LINUX_PREFIX/include/rocksdb/c.h" ] || die "未找到 RocksDB 头文件: $ROCKSDB_LINUX_PREFIX/include/rocksdb/c.h"
[ -f "$ROCKSDB_LINUX_PREFIX/lib/librocksdb.a" ] || die "未找到 RocksDB 静态库: $ROCKSDB_LINUX_PREFIX/lib/librocksdb.a"
[ -f "$GNU_RUNTIME_GCC_DEV_LIB_DIR/libstdc++.a" ] || die "未找到 Linux GNU libstdc++ 静态库: $GNU_RUNTIME_GCC_DEV_LIB_DIR/libstdc++.a"

for lib in libsnappy.a libz.a liblz4.a libzstd.a libbz2.a; do
    [ -f "$ROCKSDB_LINUX_PREFIX/lib/$lib" ] || die "缺少 Linux 静态依赖: $ROCKSDB_LINUX_PREFIX/lib/$lib"
done

echo "✓ 主机平台: macOS $(uname -m)"
echo "✓ 目标平台: linux/amd64"
echo "✓ 目标 ABI: $LINUX_TARGET"
echo "✓ C 编译器: $CC"
echo "✓ C++ 编译器: $CXX"
echo "✓ Linux RocksDB 前缀: $ROCKSDB_LINUX_PREFIX"
echo ""

rm -rf "$RELEASE_DIR/linux"
mkdir -p "$RELEASE_DIR/linux/bin" "$RELEASE_DIR/linux/config" "$RELEASE_DIR/linux/log" "$RELEASE_DIR/linux/lib"

export CGO_ENABLED=1
export GOOS=linux
export GOARCH=amd64
export CGO_CFLAGS="-I$ROCKSDB_LINUX_PREFIX/include ${CGO_CFLAGS_EXTRA:-}"
export CGO_CXXFLAGS="-I$ROCKSDB_LINUX_PREFIX/include ${CGO_CFLAGS_EXTRA:-}"
export CGO_LDFLAGS="$ROCKSDB_LINUX_PREFIX/lib/librocksdb.a -L$ROCKSDB_LINUX_PREFIX/lib -lsnappy -llz4 -lzstd -lbz2 -lz -lm -lpthread -ldl ${CGO_LDFLAGS_EXTRA:-}"

EXTLDFLAGS="-nostdlib++ $GNU_RUNTIME_GCC_DEV_LIB_DIR/libstdc++.a -L$GNU_RUNTIME_GCC_LIB_DIR -Wl,-rpath,\$ORIGIN/../lib -lgcc_s -lm -ldl -lpthread"
LDFLAGS="-X main.Version=$VERSION -X main.BuildTime=$BUILD_TIME -X main.GitCommit=$GIT_COMMIT -linkmode external -extld=$CC -extldflags '$EXTLDFLAGS'"

log_step "步骤 1: 编译"
go build -modfile="$BUILD_MODFILE" -tags "grocksdb_no_link" -ldflags "$LDFLAGS" -o "$RELEASE_DIR/linux/bin/$APP_NAME" .

log_step "步骤 2: 复制配置和脚本"
cp -r "$CONFIG_DIR"/* "$RELEASE_DIR/linux/config/" 2>/dev/null || true
if [ -f "$GNU_RUNTIME_STDCXX_LIB_DIR/libstdc++.so.6" ]; then
    cp -P "$GNU_RUNTIME_STDCXX_LIB_DIR"/libstdc++.so.6* "$RELEASE_DIR/linux/lib/" 2>/dev/null || true
fi
if [ -f "$GNU_RUNTIME_GCC_LIB_DIR/libgcc_s.so.1" ]; then
    cp -P "$GNU_RUNTIME_GCC_LIB_DIR"/libgcc_s.so.1* "$RELEASE_DIR/linux/lib/" 2>/dev/null || true
fi
make create-unix-scripts PLATFORM_DIR="$RELEASE_DIR/linux"

log_step "步骤 3: 验证产物"
FILE_INFO="$(file "$RELEASE_DIR/linux/bin/$APP_NAME")"
echo "$FILE_INFO"
ls -lh "$RELEASE_DIR/linux/bin/$APP_NAME"

echo "$FILE_INFO" | grep -q "ELF 64-bit.*x86-64" || die "产物看起来不是 Linux amd64 ELF 文件，请检查交叉编译器配置"

if command -v readelf >/dev/null 2>&1; then
    echo "GLIBC 版本需求:"
    readelf -V "$RELEASE_DIR/linux/bin/$APP_NAME" | grep -o "GLIBC_[0-9.]*" | sort -Vu | tail -20 || true
fi

create_archives

echo ""
echo "✓ 交叉构建完成: $RELEASE_DIR/linux/bin/$APP_NAME"
echo "提示: 请在 Linux 机器上运行 'ldd $APP_NAME' 和 '$APP_NAME -h' 做最终验证。"
