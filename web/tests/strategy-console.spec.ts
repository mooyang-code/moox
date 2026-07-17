import { expect, test } from '@playwright/test';
import { installE2ESession } from './e2e-session';

test('strategy overview shows running strategy without source or rollback controls', async ({ page }) => {
  await page.route('**/api/admin/auth/GetUserInfo', async (route) => {
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ ret_info: { code: 0, msg: 'ok' }, user_info: { user_id: 'u1', username: 'admin', nickname: 'Admin', role: 2 } }) });
  });
  await page.route('**/api/admin/space/ListSpaces', route =>
    route.fulfill({ json: { ret_info: { code: 0, msg: 'ok' }, spaces: [{ space_id: 'space-1', name: '测试空间', status: 'active' }], page_result: { page: 1, size: 20, total: 1, has_more: false } } }),
  );
  await page.route('**/api/admin/strategy/ListRunningStrategies', async (route) => {
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ ret_info: { code: 0, msg: 'ok' }, items: [{ strategy_id: 'momentum', version: '1.0.0', binding_id: 'binding-paper', space_id: 'space-1', mode: 'paper', status: 'enabled', source_hash: 'hash-momentum', health: { status: 'running', mode: 'paper', worker_status: 'ready' } }], total: 1, page: 1, page_size: 20 }) });
  });
  await installE2ESession(page, 'space-1');
  await page.goto('/#/strategy/overview');
  await expect(page.getByRole('heading', { name: '策略运行概览' })).toBeVisible();
  await expect(page.getByText('momentum@1.0.0')).toBeVisible();
  await expect(page.getByText('源码编辑')).toHaveCount(0);
  await expect(page.getByText('版本回滚')).toHaveCount(0);
});
