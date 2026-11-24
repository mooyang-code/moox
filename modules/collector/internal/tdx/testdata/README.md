# TDX wire fixture policy

离线单元测试使用测试代码构造的最小帧，验证帧边界、压缩解码和字段偏移。
生产线路抓包不会提交到仓库，`cmd/wire-spike` 会把原始请求、响应和报告写入
调用方指定的临时目录。

只有同时具备完整请求流、每个 16 字节响应头、压缩/解压 Body、解析结果和人工
逐字段对账记录的样本，才可以转换为仓库 fixture，并提升对应 Source 的状态。
normal `7709`、extended classic `7727` 和 extended MAC `7727` 必须分别验收。
