// Package cloud 是 Collector 内部调腾讯云的包（原 CloudFunction 模块）：云函数的读回与幂等部署、定时器、调用与清点，
// 代码包与云账号，以及经网关读 Admin 的云账号密钥、CLS 上报环境补齐。
//
// 只有控制面（采集计划的对账器）引用它；云函数程序与 collector-stream 不得引用（见 internal/depcheck）。
//
// # 给调用方（批 2 的 T5：对账器与操作表）的约束
//
//   - 停用定时器、退役函数一律走 SetTimer，不走 Ensure：SetTimer 停用时不等 Active，函数在 Updating、UpdateFailed 时也能停，
//     CreateFailed 时直接成功（见下面的实测第 1 条）；Ensure 要先把函数修到 Active，修不好就停不了。
//   - UpgradeCode 遇到 UpdateFailed（以及别的失败状态）会报错，要退回 Ensure：只有 Ensure 会重做更新、删掉重建。
//   - FunctionSpec.Package 与 UpgradeCode 的包在操作开始之前现取（Packages.Latest）：操作排队期间可能上传了新包，
//     旧包也可能已被清理（每个包名只留最新 3 个）。
//   - Ensure 因为空间正在暂停（FunctionSpec.Pausing 返回 true）把定时器设成停用时仍返回成功；操作表不能把这次成功
//     当成本阶段已确认，恢复后要重新推一次。
//   - 删云账号之前除了看 scf_plan 配置与代码包（Accounts.Delete 只看代码包），还要考虑云上仍有这个账号建的函数：
//     账号删掉后这些函数没人管，定时器会一直开着。
//   - 配额超限返回 ErrQuota（只认 LimitExceeded.Function、LimitExceeded.Namespace、LimitExceeded.FunctionReservedConcurrencyMemory），
//     失败原因报 quota；其他 LimitExceeded.* 是参数越界，按 cloud 报。
//   - 读回（Describe、DescribeCode）带出的环境已经去掉密钥项；不要拿它当底去写函数，写入只经本包的 Ensure、UpgradeCode。
//
// # 2026-11-14 真机实测结论（广州 default 命名空间，临时函数已删）
//
//  1. 函数处于 Updating、UpdateFailed、CreateFailed 时能否改定时器：
//     - Updating（改配置后、换代码后立刻）：已确认，停用被接受，回到 Active 后定时器仍是关的。
//     - UpdateFailed：已确认，停用、启用都被接受。
//     - CreateFailed：不成立。腾讯云拒绝一切触发器操作（FailedOperation，“functionInfo Status is CreateFailed, unsupport operate”）。
//     原先 SetTimer(停用) 在没有定时器时会去建一个关着的定时器，在 CreateFailed 上失败并重试到超时，暂停会卡住。
//     修法：SetTimer 停用时，函数是 CreateFailed 就直接成功（建定时器要求函数正常，CreateFailed 的函数不可能有定时器）；
//     其他状态先读定时器，没有就直接成功（没有定时器就不会触发）。读回（Describe）在 CreateFailed 时不读定时器与异步、预留配置；
//     Delete 在 CreateFailed 时跳过删定时器。Ensure 的路径不变（CreateFailed 先删再建，不碰触发器）。
//  2. UpdateFailed 时能否改配置、换代码：已确认。改配置被接受、回到 Active；用正常代码包换代码也被接受、回到 Active，
//     所以 Ensure 对 UpdateFailed 重做更新可行。诱发办法：用不存在的 COS 对象换代码，约 6 秒后进入 UpdateFailed。
//  3. 每次改配置都带同样的 VPC、公网、并发会不会让 Updating 变长：未测（探针漏传 DynamicEnabled，广州也没有对应的 VPC）。
//     只影响快慢；现在只在配置不一致时才带这些字段。
//  4. 写异步重试、预留并发会不会让函数短暂进入 Updating：已确认不会，写完 5 秒内一直是 Active（第 5 步读回前仍等 Active，无害）。
//  5. 从没配置过时两个读接口返回什么：
//     - GetFunctionEventInvokeConfig 返回成功，内容是默认值 RetryConfig=[{RetryNum:2},{RetryNum:-1}]、MsgTTL=21600，
//     不是 ResourceNotFound。读回只看第一项，解析成“已配置、RetryNum=2”，与期望（0、60）不一致而写一次。
//     - GetReservedConcurrencyConfig 返回成功、ReservedMem 为 null，判为没配置（0）。
//  6. 配额错误码：命名空间满了 CreateNamespace 返回 LimitExceeded.Namespace（已确认）；预留并发超出账号总额（128000 MB）返回
//     LimitExceeded.FunctionReservedConcurrencyMemory（已确认，账号级配额，算进 ErrQuota、报 quota）；内存越界返回
//     LimitExceeded.Memory（已确认，不算配额）；函数个数满的 LimitExceeded.Function 未测，按文档保留。
//  7. 删除 CreateFailed 的函数：已确认被接受，约 4.6 秒后函数就不存在了（用不存在的 COS 对象建函数，约 8 秒后进入 CreateFailed）。
//
// 其他实测：函数不存在时 GetFunction、ListTriggers、GetFunctionEventInvokeConfig、GetReservedConcurrencyConfig、DeleteFunction
// 都返回 ResourceNotFound.Function，命名空间不存在时返回 ResourceNotFound.Namespace；改配置时环境整份替换（只带 {A=1} 时
// 云上原有的 B 被删掉），所以废弃键第一次写就会删掉。耗时：建函数约 8.5 秒到 Active，只改环境时 Updating 约 2.7～5.2 秒，
// 换代码约 2 秒。
package cloud
