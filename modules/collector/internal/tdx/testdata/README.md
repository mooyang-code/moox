# TDX wire fixture policy

离线单元测试使用测试代码构造的最小帧，验证帧边界、压缩解码和字段偏移；生产线路原始抓包不提交到仓库。

只有同时具备完整请求流、每个 16 字节响应头、压缩/解压 Body、解析结果和人工
逐字段对账记录的样本，才可以转换为仓库 fixture，并提升对应 Source 的状态。
normal `7709`、extended classic `7727` 和 extended MAC `7727` 必须分别验收。
