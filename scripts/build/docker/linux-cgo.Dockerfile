# 本地编译 Linux 二进制（含 CGO / DuckDB）用的编译镜像，由 scripts/build/build-linux-local.sh 构建和使用。
#
# 基础系统用 Ubuntu 22.04：编出的二进制依赖的 glibc / libstdc++ 版本较低，能在更新的线上系统上运行。
# 宿主是 arm64（Apple 芯片的 Mac）时，镜像里是 arm64 的编译器，用交叉编译器产出 linux/amd64，不需要模拟 x86。
FROM ubuntu:22.04

ARG GO_VERSION
ARG GO_SHA256_AMD64
ARG GO_SHA256_ARM64
ARG GO_DL_BASE=https://golang.google.cn/dl
# 国内网络可设为 http://mirrors.aliyun.com ，会同时替换 amd64 与 arm64 的软件源（中科大、清华的 http 镜像也可以）。
# 必须用不会跳转到 https 的 http 镜像：基础镜像里还没有 ca-certificates，https 源取不到索引（软件包本身有 GPG 签名校验）；
# 腾讯云镜像会把 http 重定向到 https，所以不能用。
ARG APT_MIRROR=

ENV DEBIAN_FRONTEND=noninteractive

RUN set -eux; \
    if [ -n "${APT_MIRROR}" ]; then \
        sed -i -e "s#http://archive.ubuntu.com/ubuntu#${APT_MIRROR}/ubuntu#g" \
               -e "s#http://ports.ubuntu.com/ubuntu-ports#${APT_MIRROR}/ubuntu-ports#g" \
               -e "s#http://security.ubuntu.com/ubuntu#${APT_MIRROR}/ubuntu#g" /etc/apt/sources.list; \
    fi; \
    arch="$(dpkg --print-architecture)"; \
    cross=""; \
    if [ "${arch}" = arm64 ]; then cross="gcc-x86-64-linux-gnu g++-x86-64-linux-gnu"; fi; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates curl git gcc g++ make ${cross}; \
    rm -rf /var/lib/apt/lists/*

RUN set -eux; \
    arch="$(dpkg --print-architecture)"; \
    case "${arch}" in amd64) sha="${GO_SHA256_AMD64}" ;; arm64) sha="${GO_SHA256_ARM64}" ;; *) echo "不支持的架构 ${arch}" >&2; exit 1 ;; esac; \
    file="go${GO_VERSION}.linux-${arch}.tar.gz"; \
    curl -fsSL "${GO_DL_BASE}/${file}" -o /tmp/go.tgz; \
    echo "${sha}  /tmp/go.tgz" | sha256sum -c -; \
    tar -C /usr/local -xzf /tmp/go.tgz; \
    rm /tmp/go.tgz

ENV PATH=/usr/local/go/bin:${PATH} \
    GOTOOLCHAIN=local \
    GOFLAGS=-buildvcs=false

WORKDIR /src
