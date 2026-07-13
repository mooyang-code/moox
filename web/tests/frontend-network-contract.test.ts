import { readFileSync, readdirSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

const root = resolve(__dirname, '..');
function sourceFiles(directory: string): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const path = resolve(directory, entry.name);
    if (entry.isDirectory()) return sourceFiles(path);
    return /\.(ts|vue)$/.test(entry.name) && !entry.name.endsWith('.test.ts') ? [path] : [];
  });
}

const source = sourceFiles(resolve(root, 'src')).map((file) => readFileSync(file, 'utf8')).join('\n');

describe('frontend network contract', () => {
  it('does not construct service bypass or direct Admin gateway URLs', () => {
    expect(source).not.toMatch(/\/api\/service\//);
    expect(source).not.toMatch(/11000|VITE_GATEWAY_PORT|DEFAULT_GATEWAY_PORT/);
  });

  it('keeps auth and raw routes under the same-origin Admin gateway', () => {
    expect(source).toContain('/api/admin/auth/GetLoginSalt');
    expect(source).toContain('/api/admin/auth/Login');
    expect(source).toContain('/api/admin/ssh/WsConnect');
    expect(source).toContain('/api/admin/ssh/SftpDownload');
    expect(source).toContain('/api/admin/ssh/SftpUpload');
  });

  it('uploads to external pre-signed URLs without session credentials', () => {
    const uploadSource = readFileSync(resolve(root, 'src/api/function-package.ts'), 'utf8');
    expect(uploadSource).toContain("fetch(initRsp.upload_url, { method: 'PUT', body: file })");
    expect(uploadSource).not.toMatch(/upload_url[\s\S]{0,200}(Authorization|X-Access-Token|X-Moox-|X-Moox-Space)/);
  });
});
