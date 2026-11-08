import { createPinia, setActivePinia } from "pinia";

// 页面组件读空间时区（useDisplayZone → useSpaceStore）。在这里给每个测试文件放一个默认的 Pinia：
// 没有安装 Pinia 的组件测试也能挂载；需要控制 store 内容的测试照旧在自己的 beforeEach 里 setActivePinia 覆盖它。
setActivePinia(createPinia());
