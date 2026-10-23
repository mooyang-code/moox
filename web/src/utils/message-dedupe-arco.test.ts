import { afterEach, describe, expect, it } from "vitest";
import { Message } from "@arco-design/web-vue";
import { installMessageDedupe } from "./message-dedupe";

// 用真实的 Arco Message 验证：同一条文案连续报错只显示一条（而不是每个失败请求各弹一条把页面刷满）。
describe("真实 Arco Message 下的去重", () => {
  afterEach(async () => {
    // 只交给 Arco 自己清理：手工清空 body 后，Arco 随后的移除动画会对已经不在的节点调用 removeChild，
    // 在 CI 机器负载高时变成「The node to be removed is not a child of this node」未处理错误，让整个 test-web 失败。
    Message.clear();
    await new Promise(resolve => setTimeout(resolve, 100));
  });

  it("相同文案连续弹 30 次只留下 1 条，不同文案各 1 条", async () => {
    installMessageDedupe();
    for (let i = 0; i < 30; i++) Message.error("Network Error");
    Message.error("另一种错误");
    await new Promise(resolve => setTimeout(resolve, 50));
    const texts = Array.from(document.querySelectorAll(".arco-message")).map(node => node.textContent?.trim());
    expect(texts.filter(text => text === "Network Error")).toHaveLength(1);
    expect(texts.filter(text => text === "另一种错误")).toHaveLength(1);
  });
});
