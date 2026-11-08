// 录入页「交易所密钥」一步的行为检查：在 jsdom 里加载 page.html，验证没配海外服务器时隐藏、前进后退跳过、
// 隐藏时不发送密钥、步骤条编号和密钥行的校验。由 page_exchange_test.go 运行（node 或 jsdom 不存在时跳过）。
// 用法：node page_exchange.cjs <jsdom 路径> <page.html 路径> <boot JSON 路径>
const fs = require('fs');
const { JSDOM } = require(process.argv[2]);
const html0 = fs.readFileSync(process.argv[3], 'utf8');
const boot = JSON.parse(fs.readFileSync(process.argv[4], 'utf8'));
let html = html0.replace('{{.Boot}}', JSON.stringify(Object.assign({ deadline: Date.now() + 3600000, key_store: '系统钥匙串' }, boot))).replace('{{.Nonce}}', 'n');
// 页面脚本在闭包里，测试时把要检查的函数挂到 window 上。
html = html.replace("  renderServers();\n  $('add-exchange-key')", "  window.__t = { go: go, neighbor: neighbor, visibleSteps: visibleSteps, gather: gather, exchangeEntries: exchangeEntries, checkExchangeKeys: checkExchangeKeys, addExchangeKey: addExchangeKey, state: state, blocks: blocks };\n  renderServers();\n  $('add-exchange-key')");
if (!html.includes('window.__t')) throw new Error('注入检查入口失败：page.html 里的锚点变了');
const dom = new JSDOM(html, { runScripts: 'dangerously', pretendToBeVisual: true, url: 'http://127.0.0.1/' });
const w = dom.window, d = w.document, t = w.__t;
w.scrollTo = function () {};
let failed = 0;
function eq(name, got, want) { const ok = JSON.stringify(got) === JSON.stringify(want); if (!ok) failed++; console.log((ok ? 'ok   ' : 'FAIL ') + name + (ok ? '' : ' got=' + JSON.stringify(got) + ' want=' + JSON.stringify(want))); }
const stepsText = () => Array.from(d.querySelectorAll('#steps li')).map(li => li.textContent.replace(/\s+/g, ' ').trim());
const visiblePage = () => Array.from(d.querySelectorAll('.page')).filter(p => !p.hidden).map(p => p.id);

// 1. 关掉海外：没有交易所密钥这一步
eq('overseas toggle is on by default', t.blocks.overseas.toggle.checked, true);
t.blocks.overseas.toggle.checked = false;
t.blocks.overseas.toggle.dispatchEvent(new w.Event('change'));
eq('visible steps without overseas', t.visibleSteps(), [1, 2, 3, 4, 6]);
t.go(1);
eq('step list without overseas', stepsText().map(s => s.replace(/^[✓\d]+ ?/, '')), ['腾讯云账号', '服务器', '免密登录', '管理员与告警', '确认']);
eq('forward from admin skips exchange', t.neighbor(4, 1), 6);
eq('back from review skips exchange', t.neighbor(6, -1), 4);
eq('gather sends empty exchange_keys', t.gather().exchange_keys, []);

// 即使页面上填了密钥，隐藏时也不发送
const row = t.addExchangeKey();
row.name.value = 'readonly'; row.apiKey.value = 'ak'; row.secretKey.value = 'sk'; row.readonly.checked = true;
row.name.dispatchEvent(new w.Event('input'));
eq('hidden step never sends keys', t.gather().exchange_keys, []);
eq('fixed id preview', d.querySelector('.xkey .id').textContent, '固定 ID：exchange-binance-production-readonly');

// 2. 勾上海外：出现这一步，步骤条同步变化
t.blocks.overseas.toggle.checked = true;
t.blocks.overseas.toggle.dispatchEvent(new w.Event('change'));
eq('visible steps with overseas', t.visibleSteps(), [1, 2, 3, 4, 5, 6]);
eq('step list updates on toggle', stepsText().length, 6);
eq('forward from admin goes to exchange', t.neighbor(4, 1), 5);
eq('back from review goes to exchange', t.neighbor(6, -1), 5);
eq('sends the entered key', t.gather().exchange_keys, [{ name: 'readonly', exchange: 'binance', environment: 'production', api_key: 'ak', secret_key: 'sk', passphrase: '', readonly: true }]);
t.go(5);
eq('exchange page shown', visiblePage(), ['page-5']);
eq('confirm button label on step 5', d.getElementById('next').textContent, '下一步');
const fnd = { instance: { region_name: 'r', kind_name: 'k' } };
t.state.found = { storage: Object.assign({ address: '1.1.1.1' }, fnd), control: Object.assign({ address: '2.2.2.2' }, fnd), overseas: Object.assign({ address: '3.3.3.3' }, fnd) };
t.go(6);
eq('confirm page shown', visiblePage(), ['page-6']);
eq('confirm button label', d.getElementById('next').textContent, '确认并完成');
eq('review lists the key', d.getElementById('review-account').textContent.includes('exchange-binance-production-readonly'), true);
t.go(7);
eq('finish page is page-7', visiblePage(), ['page-7']);

// 3. 校验
eq('valid key passes', t.checkExchangeKeys(), '');
row.name.value = 'Bad_Name';
eq('bad name rejected', t.checkExchangeKeys().includes('名称只能用'), true);
row.name.value = 'readonly';
row.exchange.value = 'okx'; row.exchange.dispatchEvent(new w.Event('change'));
eq('okx shows passphrase field', d.querySelector('.xkey .fields > label:nth-of-type(6)').hidden, false);
eq('okx without passphrase rejected', t.checkExchangeKeys().includes('Passphrase'), true);
row.passphrase.value = 'pp';
eq('okx with passphrase passes', t.checkExchangeKeys(), '');
eq('passphrase sent for okx', t.gather().exchange_keys[0].passphrase, 'pp');
row.exchange.value = 'binance'; row.exchange.dispatchEvent(new w.Event('change'));
eq('passphrase dropped for binance', t.gather().exchange_keys[0].passphrase, '');
const row2 = t.addExchangeKey(); row2.name.value = 'readonly'; row2.apiKey.value = 'a2'; row2.secretKey.value = 's2';
eq('duplicate id rejected', t.checkExchangeKeys().includes('重复'), true);
row2.environment.value = 'testnet';
eq('different environment is not a duplicate', t.checkExchangeKeys(), '');
// 整行为空的行被忽略
const blank = t.addExchangeKey();
eq('blank rows ignored', t.exchangeEntries().length, 2);
// 删除
blank.node.querySelector('button').click();
eq('delete removes the row', d.querySelectorAll('.xkey').length, 2);
process.exit(failed ? 1 : 0);
