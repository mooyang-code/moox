package moox

import _ "embed"

// ExampleConfigTemplate 是仓库根目录的 moox.toml.example 原文，供 CLI 配置初始化直接嵌入，避免仓库内维护副本。
//
//go:embed moox.toml.example
var ExampleConfigTemplate string

// ExampleSecretsTemplate 是仓库根目录的 moox.secrets.toml.example 原文，供 CLI 配置初始化直接嵌入，避免仓库内维护副本。
//
//go:embed moox.secrets.toml.example
var ExampleSecretsTemplate string
