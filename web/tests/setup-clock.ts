// 让 Date.now() 在测试里不往回走。
// Vue 给事件处理函数记下挂载时刻（Date.now()），触发时刻不晚于挂载时刻的事件直接丢弃；@vue/test-utils 的 trigger 用
// Date.now() + 1 作为触发时刻。本机 Docker（colima 虚拟机）的系统时钟每 10 秒左右会被往回拨约 0.5 秒，
// 挂载和点击正好跨过这一下时，点击被丢掉，表现为按钮可点、点了却没反应的偶发失败。
// 用了假定时器的测试不受影响：vi.useFakeTimers 会换掉整个 Date。
const systemNow = Date.now.bind(Date);
let latest = 0;
Date.now = () => (latest = Math.max(latest, systemNow()));
