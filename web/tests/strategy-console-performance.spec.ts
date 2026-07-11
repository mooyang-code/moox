import { expect, test } from '@playwright/test';

test('strategy overview requests a bounded page instead of full history', async ({ page }) => {
  let requestBody: { page?: { page_size?: number } } | null = null;
  await page.route('**/api/admin/auth/GetUserInfo', (route) => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ ret_info: { code: 0 }, user_info: { user_id: 'u1', username: 'admin', role: 2 } }) }));
  await page.route('**/api/admin/strategy/ListRunningStrategies', async (route) => {
    requestBody = route.request().postDataJSON() as { page?: { page_size?: number } };
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ ret_info: { code: 0 }, items: [], total: 10000, page: 1, page_size: 20 }) });
  });
  await page.addInitScript(() => localStorage.setItem('user-info', JSON.stringify({ token: 'test-token' })));
  await page.goto('/#/strategy/overview');
  await expect(page.getByRole('heading', { name: '策略运行概览' })).toBeVisible();
  expect(requestBody?.page?.page_size).toBe(20);
});
