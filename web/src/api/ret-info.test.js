import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import ts from 'typescript';

const source = await readFile(new URL('./ret-info.ts', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(source, {
  compilerOptions: {
    module: ts.ModuleKind.ES2020,
    target: ts.ScriptTarget.ES2020,
  },
});
const moduleUrl = `data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`;
const { isAuthExpiredCode, isRetInfoSuccess } = await import(moduleUrl);

const successCodes = [0, '0', 200, '200', 'SUCCESS'];
for (const code of successCodes) {
  assert.equal(isRetInfoSuccess(code), true, `${String(code)} should be treated as success`);
}

const failureCodes = [1, '1', 3, '3', 401, '401', 'FAILED', undefined, null];
for (const code of failureCodes) {
  assert.equal(isRetInfoSuccess(code), false, `${String(code)} should not be treated as success`);
}

const expiredCodes = [3, '3', 401, '401', 'TOKEN_EXPIRED', 'UNAUTHORIZED', 'AUTH_EXPIRED'];
for (const code of expiredCodes) {
  assert.equal(isAuthExpiredCode(code), true, `${String(code)} should be treated as auth expired`);
}

const activeCodes = [0, '0', 200, '200', 'SUCCESS', 404, 'FAILED', undefined, null];
for (const code of activeCodes) {
  assert.equal(isAuthExpiredCode(code), false, `${String(code)} should not be treated as auth expired`);
}

console.log('ret_info helper tests passed');
