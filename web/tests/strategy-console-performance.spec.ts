import { expect, test } from '@playwright/test';
import { installE2ESession } from './e2e-session';

test('strategy overview requests a bounded page instead of full history', async ({ page }) => {
  let requestBody: { page?: { page_size?: number } } | null = null;
  await page.route('**/api/admin/auth/GetUserInfo', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ ret_info: { code: 0 }, user_info: { user_id: 'u1', username: 'admin', role: 2 } }) }));
  await page.route('**/api/admin/space/ListSpaces', route => route.fulfill({ json: { ret_info: { code: 0 }, spaces: [{ space_id: 'space-1', name: '测试空间', status: 'active' }], page_result: { page: 1, size: 20, total: 1, has_more: false } } }));
  await page.route('**/api/admin/strategy/ListRunningStrategies', async (route) => {
    requestBody = route.request().postDataJSON() as { page?: { page_size?: number } };
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ ret_info: { code: 0 }, items: [], total: 10000, page: 1, page_size: 20 }) });
  });
  await installE2ESession(page, 'space-1');
  await page.goto('/#/strategy/overview');
  await expect(page.getByRole('heading', { name: '策略运行概览' })).toBeVisible();
  expect(requestBody?.page?.page_size).toBe(20);
});
