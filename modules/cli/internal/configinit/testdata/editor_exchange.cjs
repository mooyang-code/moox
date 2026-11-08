// 配置页「交易所密钥」的行为检查：在 jsdom 里加载 editor.html，验证概览只显示打码信息并按序号取明文、
// 表单能增删改密钥并把完整列表交给保存接口。由 page_exchange_test.go 运行。
// 用法：node editor_exchange.cjs <jsdom 路径> <editor.html 路径>
const fs = require('fs');
const { JSDOM } = require(process.argv[2]);
let html = fs.readFileSync(process.argv[3], 'utf8').replace('{{.Boot}}', JSON.stringify({ deadline: Date.now() + 3600000 })).replace('{{.Nonce}}', 'n');
const overview = {
  encrypted: true, key_store: '测试', servers: [], secret_id: 'AKIDxx••••', region: '广州', admin: 'admin', channel: '企业微信群机器人', has_webhook: true,
  exchange_keys: [
    { index: 0, name: 'readonly', exchange: 'binance', environment: 'production', readonly: true, secret_id: 'exchange-binance-production-readonly', api_key: 'ro-k••••', has_passphrase: false },
    { index: 1, name: 'main', exchange: 'okx', environment: 'testnet', readonly: false, secret_id: 'exchange-okx-testnet-main', api_key: 'okx••••', has_passphrase: true },
  ],
};
const form = {
  admin: { username: 'admin', password: 'p' }, tencent: { secret_id: 'i', secret_key: 'k' }, notification: { channel_type: 'wecom', webhook_url: '' },
  exchange_keys: [
    { name: 'readonly', exchange: 'binance', environment: 'production', api_key: 'ro-key', secret_key: 'ro-secret', passphrase: '', readonly: true },
    { name: 'main', exchange: 'okx', environment: 'testnet', api_key: 'okx-key', secret_key: 'okx-secret', passphrase: 'okx-pass', readonly: false },
  ],
  hosts: [], retention: { view_rows_per_series: 1, max_view_file_bytes: 1, frequencies: [], defaults: {}, spaces: [], recommended: { defaults: {}, spaces: {} } }, logs: { max_size_mb: 50, backup_count: 5 },
};
const calls = [];
const dom = new JSDOM(html, {
  runScripts: 'dangerously', pretendToBeVisual: true, url: 'http://127.0.0.1/',
  beforeParse(window) {
    window.scrollTo = function () {};
    window.fetch = function (path, init) {
      const body = JSON.parse((init && init.body) || '{}');
      calls.push({ path, body });
      let data = { ok: true };
      if (path === 'config/overview') data.overview = overview;
      else if (path === 'config/form') data.form = form;
      else if (path === 'config/reveal') data.value = 'plain:' + body.field;
      else if (path === 'config/update') { data.overview = overview; data.sync = ''; }
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(data) });
    };
  },
});
const w = dom.window, d = w.document;
let failed = 0;
function eq(name, got, want) { const ok = JSON.stringify(got) === JSON.stringify(want); if (!ok) failed++; console.log((ok ? 'ok   ' : 'FAIL ') + name + (ok ? '' : ' got=' + JSON.stringify(got) + ' want=' + JSON.stringify(want))); }
const tick = () => new Promise(r => setTimeout(r, 30));
(async () => {
  await tick();
  eq('overview block shown', d.getElementById('xkeys-block').hidden, false);
  const rows = Array.from(d.querySelectorAll('#xkeys tbody tr'));
  eq('overview rows', rows.length, 2);
  eq('overview shows masked api key only', rows[0].textContent.includes('ro-k••••') && !rows[0].textContent.includes('ro-key'), true);
  eq('overview shows fixed id', rows[1].textContent.includes('exchange-okx-testnet-main'), true);
  eq('passphrase reveal only when present', rows[0].querySelectorAll('button').length, 2);
  eq('okx row has three reveal buttons', rows[1].querySelectorAll('button').length, 3);
  rows[1].querySelectorAll('button')[2].click();
  await tick();
  eq('reveal asks by index and field', calls.filter(c => c.path === 'config/reveal').map(c => c.body.field), ['exchange_keys.1.passphrase']);

  d.querySelector('#nav li[data-page="edit"]').click();
  await tick();
  const cards = () => Array.from(d.querySelectorAll('#form .xkey'));
  eq('form renders both keys', cards().length, 2);
  eq('okx passphrase visible, binance hidden', [cards()[0].querySelector('.fields > label:nth-of-type(6)').hidden, cards()[1].querySelector('.fields > label:nth-of-type(6)').hidden], [true, false]);
  // 改第一把的 API Key、删第二把、再加一把
  const firstApi = cards()[0].querySelectorAll('input[type=password]')[0];
  firstApi.value = 'ro-key-2';
  cards()[1].querySelector('button.link').click();
  eq('delete removes the card', cards().length, 1);
  const add = Array.from(d.querySelectorAll('#form button')).find(b => b.textContent === '添加一把密钥');
  add.click();
  eq('add appends a card', cards().length, 2);
  const fresh = cards()[1];
  fresh.querySelector('input[type=text]').value = 'trade';
  fresh.querySelector('input[type=text]').dispatchEvent(new w.Event('input'));
  eq('fixed id preview', fresh.querySelector('.id').textContent, '固定 ID：exchange-binance-production-trade');
  const pw = fresh.querySelectorAll('input[type=password]');
  pw[0].value = 't-key'; pw[1].value = 't-secret';
  d.getElementById('save').click();
  await tick();
  const update = calls.filter(c => c.path === 'config/update').pop();
  eq('save sends the whole list', update.body.exchange_keys, [
    { name: 'readonly', exchange: 'binance', environment: 'production', api_key: 'ro-key-2', secret_key: 'ro-secret', passphrase: '', readonly: true },
    { name: 'trade', exchange: 'binance', environment: 'production', api_key: 't-key', secret_key: 't-secret', passphrase: '', readonly: false },
  ]);
  process.exit(failed ? 1 : 0);
})();
