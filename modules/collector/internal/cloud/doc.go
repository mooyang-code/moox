// Package cloud 是 Collector 内部调腾讯云的包（原 CloudFunction 模块）：云函数的读回与幂等部署、定时器、调用与清点，
// 代码包与云账号，以及经网关读 Admin 的云账号密钥、CLS 上报环境补齐。
//
// 只有控制面（采集计划的对账器）引用它；云函数程序与 collector-stream 不得引用（见 internal/depcheck）。
//
// # 给调用方（批 2 的 T5：对账器与操作表）的约束
//
//   - 停用定时器、退役函数一律走 SetTimer，不走 Ensure：SetTimer 停用时不等 Active、不看函数状态，
//     函数卡在 UpdateFailed、CreateFailed 时也能停；Ensure 要先把函数修到 Active，修不好就停不了。
//   - UpgradeCode 遇到 UpdateFailed（以及别的失败状态）会报错，要退回 Ensure：只有 Ensure 会重做更新、删掉重建。
//   - FunctionSpec.Package 与 UpgradeCode 的包在操作开始之前现取（Packages.Latest）：操作排队期间可能上传了新包，
//     旧包也可能已被清理（每个包名只留最新 3 个）。
//   - Ensure 因为空间正在暂停（FunctionSpec.Pausing 返回 true）把定时器设成停用时仍返回成功；操作表不能把这次成功
//     当成本阶段（例如“过渡计划已下发并启用”）已确认，恢复后要重新推一次。
//   - 删云账号之前除了看 scf_plan 配置与代码包（Accounts.Delete 只看代码包），还要考虑云上仍有这个账号建的函数：
//     账号删掉后这些函数没人管，定时器会一直开着。
//   - 配额超限返回 ErrQuota（只认 LimitExceeded.Function、LimitExceeded.Namespace），失败原因报 quota；
//     其他 LimitExceeded.* 是参数越界，按 cloud 报。
//   - 读回（Describe、DescribeCode）带出的环境已经去掉密钥项；不要拿它当底去写函数，写入只经本包的 Ensure、UpgradeCode。
//
// # T5 之前要在真机上确认的腾讯云接口假设（按风险从高到低）
//
//  1. 函数处于 Updating、UpdateFailed、CreateFailed 时能否改定时器（SetTimer 停用依赖它；暂停与重置的第一步靠停用）。
//  2. 函数处于 UpdateFailed 时能否改配置、换代码（Ensure 对 UpdateFailed 直接重做更新）。
//  3. 每次改配置都带同样的 VPC、公网、并发会不会让 Updating 变长（现在只在配置不一致时带）。
//  4. 写异步重试、预留并发会不会让函数短暂进入 Updating（第 5 步读回前已改成等回到 Active）。
//  5. GetFunctionEventInvokeConfig、GetReservedConcurrencyConfig 对从没配置过的函数返回什么（现在任意 ResourceNotFound.* 当作没配置）。
//  6. 配额超限的错误码是否只有 LimitExceeded.Function、LimitExceeded.Namespace（预留并发超限的错误码也要确认）。
//  7. 删除 CreateFailed 的函数是否被接受、要多久（Ensure 等删除完成最多 5 分钟）。
package cloud
