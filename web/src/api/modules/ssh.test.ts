import { beforeEach, describe, expect, it, vi } from 'vitest';

const { callControl } = vi.hoisted(() => ({ callControl: vi.fn() }));
vi.mock('@/api/admin/http', () => ({ callControl }));
vi.mock('@/api/gateway', () => ({
  gatewayURL: (path: string) => path,
  gatewayWebSocketURL: (path: string) => path,
}));

import { getSftpDownloadUrl, getSftpUploadUrl, getSSHWebSocketUrl } from './ssh';

describe('raw SSH ticket binding', () => {
  beforeEach(() => callControl.mockReset().mockResolvedValue({ ticket: 'ticket-1' }));

  it('passes the SSH session when issuing WebSocket and download tickets', async () => {
    await getSSHWebSocketUrl('ssh-1', 80, 24);
    expect(callControl).toHaveBeenCalledWith('auth', 'IssueRawSessionTicket', { operation: 'ssh_ws', session_id: 'ssh-1' });
    await getSftpDownloadUrl('ssh-2', '/tmp/a');
    expect(callControl).toHaveBeenLastCalledWith('auth', 'IssueRawSessionTicket', { operation: 'sftp_download', session_id: 'ssh-2' });
  });

  it('passes the SSH session when issuing an upload ticket', async () => {
    await getSftpUploadUrl('ssh-3');
    expect(callControl).toHaveBeenCalledWith('auth', 'IssueRawSessionTicket', { operation: 'sftp_upload', session_id: 'ssh-3' });
  });
});
