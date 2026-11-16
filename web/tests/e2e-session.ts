import type { Page } from "@playwright/test";

// 会话在 HttpOnly Cookie 里，页面不存令牌：各用例自己拦截 auth/GetUserInfo 表示已登录，这里只预置所选空间。
export async function installE2ESession(page: Page, selectedSpaceId: string) {
  await page.route("**/__e2e_session__", route =>
    route.fulfill({ contentType: "text/html", body: "<!doctype html><title>session setup</title>" })
  );
  await page.goto("/__e2e_session__");
  await page.evaluate(spaceId => {
    localStorage.setItem("spaceStore", JSON.stringify({ selectedSpaceId: spaceId, spaces: [] }));
  }, selectedSpaceId);
}
