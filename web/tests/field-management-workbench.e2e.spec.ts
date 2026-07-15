import { expect, test, type Page, type Route } from '@playwright/test';

const groups = [
  { space_id: 'stock_cn', group_id: 'market', name: '市场数据', status: 'active', sort_order: 20 },
  { space_id: 'stock_cn', group_id: 'quote', parent_group_id: 'market', name: '行情价格', status: 'active', sort_order: 10 },
  { space_id: 'stock_cn', group_id: 'trading', parent_group_id: 'market', name: '成交数据', status: 'active', sort_order: 20 },
];

let fields = [
  { space_id: 'stock_cn', group_id: 'quote', field_id: 'close', name: '收盘价', description: '每日收盘价格', value_type: 'FIELD_VALUE_TYPE_DOUBLE', unit: 'CNY', validation_rule_json: '{}', sort_order: 10, status: 'active', updated_at: '2026-07-15T10:00:00Z' },
  { space_id: 'stock_cn', group_id: 'quote', field_id: 'open', name: '开盘价', description: '每日开盘价格', value_type: 'FIELD_VALUE_TYPE_DOUBLE', unit: 'CNY', validation_rule_json: '{}', sort_order: 20, status: 'active', updated_at: '2026-07-15T09:00:00Z' },
  { space_id: 'stock_cn', group_id: 'trading', field_id: 'volume', name: '成交量', description: '每日成交数量', value_type: 'FIELD_VALUE_TYPE_DOUBLE', unit: 'share', validation_rule_json: '{}', sort_order: 10, status: 'disabled', updated_at: '2026-07-14T09:00:00Z' },
];

const ok = (data: Record<string, unknown> = {}) => ({ ret_info: { code: 0, msg: 'success' }, ...data });

async function installSession(page: Page) {
  await page.route('**/__field_e2e_session__', (route) => route.fulfill({ contentType: 'text/html', body: '<!doctype html><title>session setup</title>' }));
  await page.goto('/__field_e2e_session__');
  await page.evaluate(async () => {
    const expiresAt = Math.floor(Date.now() / 1000) + 3600;
    localStorage.setItem('user-info', JSON.stringify({ token: 'e2e-token', sessionId: 'e2e-session', expiresAt }));
    localStorage.setItem('spaceStore', JSON.stringify({ selectedSpaceId: 'stock_cn', spaces: [] }));
    const key = await crypto.subtle.importKey('raw', new TextEncoder().encode('0'.repeat(64)), { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']);
    await new Promise<void>((resolve, reject) => {
      const request = indexedDB.open('moox-request-signing', 1);
      request.onupgradeneeded = () => request.result.createObjectStore('sessions', { keyPath: 'sessionId' });
      request.onerror = () => reject(request.error);
      request.onsuccess = () => {
        const db = request.result;
        const tx = db.transaction('sessions', 'readwrite');
        tx.objectStore('sessions').put({ sessionId: 'e2e-session', key, expiresAt });
        tx.oncomplete = () => { db.close(); resolve(); };
        tx.onerror = () => reject(tx.error);
      };
    });
  });
}

async function mockGateway(route: Route) {
  const method = route.request().url().split('/').pop();
  const body = route.request().postDataJSON?.() || {};
  if (method === 'GetUserInfo') {
    return route.fulfill({ json: ok({ user_info: { user_id: 'e2e', username: 'reviewer', nickname: 'Reviewer', role: 3, status: 1 } }) });
  }
  if (method === 'ListSpaces') {
    return route.fulfill({ json: ok({ spaces: [{ space_id: 'stock_cn', name: 'A股市场', owner: 'e2e', status: 'active' }, { space_id: 'crypto', name: '加密货币', owner: 'e2e', status: 'active' }], page_result: { page: 1, size: 20, total: 2, has_more: false } }) });
  }
  if (method === 'ListFieldGroups') {
    return route.fulfill({ json: ok({ field_groups: groups, field_counts: { market: 3, quote: 2, trading: 1 }, total_field_count: 3, ungrouped_field_count: 0, page_result: { page: 1, size: 200, total: 3, has_more: false } }) });
  }
  if (method === 'ListFields') {
    let rows = fields.slice();
    if (body.group_id === 'market') rows = rows.filter((field) => field.group_id === 'market' || ['quote', 'trading'].includes(field.group_id));
    else if (body.group_id) rows = rows.filter((field) => field.group_id === body.group_id);
    if (body.keyword) rows = rows.filter((field) => `${field.field_id}${field.name}${field.description}`.toLowerCase().includes(String(body.keyword).toLowerCase()));
    if (body.status) rows = rows.filter((field) => field.status === body.status);
    return route.fulfill({ json: ok({ fields: rows, page_result: { page: 1, size: 20, total: rows.length, has_more: false } }) });
  }
  if (method === 'BatchUpdateFields') {
    fields = fields.map((field) => body.field_ids.includes(field.field_id) ? { ...field, group_id: body.target_group_id || field.group_id, status: body.target_status || field.status } : field);
    return route.fulfill({ json: ok({ updated_count: body.field_ids.length }) });
  }
  if (['CreateField', 'UpdateField'].includes(method || '')) return route.fulfill({ json: ok({ field: body.field }) });
  if (['CreateFieldGroup', 'UpdateFieldGroup'].includes(method || '')) return route.fulfill({ json: ok({ field_group: body.field_group }) });
  if (method === 'DeleteFieldGroup') return route.fulfill({ json: ok() });
  return route.fulfill({ json: ok() });
}

test.beforeEach(async ({ page }) => {
  fields = fields.map((field) => field.field_id === 'volume' ? { ...field, status: 'disabled', group_id: 'trading' } : { ...field, status: 'active', group_id: 'quote' });
  await installSession(page);
  await page.route(/\/api\/admin\/[^/]+\/[^/?#]+(?:\?|$)/, mockGateway);
});

test('supports the approved desktop governance workflow', async ({ page }, testInfo) => {
  await page.goto('/#/data/fields');
  await expect(page.getByRole('heading', { name: '字段管理' })).toHaveCount(0);
  const searchBox = page.getByPlaceholder('搜索字段 ID、中文名或描述');
  const createButton = page.getByRole('button', { name: '新建字段' });
  const [searchBounds, createBounds] = await Promise.all([searchBox.boundingBox(), createButton.boundingBox()]);
  const searchCenter = (searchBounds?.y || 0) + (searchBounds?.height || 0) / 2;
  const createCenter = (createBounds?.y || 0) + (createBounds?.height || 0) / 2;
  expect(Math.abs(searchCenter - createCenter)).toBeLessThanOrEqual(2);
  await expect(page.getByText('收盘价', { exact: true })).toBeVisible();
  await expect(page.getByText('3 个字段')).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath('field-workbench-desktop.png'), fullPage: true });

  await page.getByText('收盘价', { exact: true }).click();
  await expect(page.getByText('编辑字段', { exact: true })).toBeVisible();
  await expect(page.getByPlaceholder('例如 close')).toBeDisabled();
  await page.getByRole('button', { name: '取消' }).last().click();

  await page.getByRole('row', { name: /收盘价 close/ }).getByRole('checkbox').locator('..').click();
  await expect(page.getByText(/已选择\s*1\s*个字段/)).toBeVisible();
  await page.getByRole('button', { name: '停用' }).click();
  await page.getByRole('button', { name: '确定' }).click();
  await expect(page.getByText(/已选择\s*1\s*个字段/)).toBeHidden();

  await page.getByPlaceholder('搜索字段 ID、中文名或描述').fill('open');
  await expect(page).toHaveURL(/keyword=open/);
  await expect(page.getByText('开盘价', { exact: true })).toBeVisible();
  await expect(page.getByText('收盘价', { exact: true })).toBeHidden();
  await page.reload();
  await expect(page.getByPlaceholder('搜索字段 ID、中文名或描述')).toHaveValue('open');
});

test('keeps controls usable on a narrow viewport', async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto('/#/data/fields');
  await expect(page.getByRole('button', { name: '字段组' })).toBeVisible();
  await page.getByRole('button', { name: '字段组' }).click();
  await expect(page.locator('.arco-drawer').getByText('全部字段', { exact: true })).toBeVisible();
  await page.keyboard.press('Escape');
  await page.getByText('收盘价', { exact: true }).click();
  const editor = page.locator('.arco-drawer').filter({ hasText: '编辑字段' });
  await expect(editor).toBeVisible();
  const box = await editor.boundingBox();
  expect(box?.width || 0).toBeLessThanOrEqual(391);
  await page.screenshot({ path: testInfo.outputPath('field-workbench-mobile.png'), fullPage: true });
});

test('guards dirty edits when switching spaces', async ({ page }) => {
  await page.goto('/#/data/fields');
  await page.getByText('收盘价', { exact: true }).click();
  const nameInput = page.locator('.arco-drawer input').nth(1);
  await nameInput.fill('未保存名称');
  await page.getByRole('textbox', { name: 'A股市场' }).locator('..').click();
  await page.getByText('加密货币', { exact: true }).click();
  await expect(page.getByText('切换空间并放弃修改？')).toBeVisible();
  await page.getByRole('button', { name: '取消' }).last().click();
  await expect(page.locator('.arco-select-view-single[title="A股市场"]')).toBeVisible();
  await expect(nameInput).toHaveValue('未保存名称');
});
