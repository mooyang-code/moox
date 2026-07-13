const DB_NAME = 'moox-request-signing';
const STORE_NAME = 'sessions';
const VERSION = 'moox-request-v1';

interface StoredSigningSession {
  sessionId: string;
  key: CryptoKey;
  expiresAt: number;
}

function openDatabase(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(DB_NAME, 1);
    request.onupgradeneeded = () => request.result.createObjectStore(STORE_NAME, { keyPath: 'sessionId' });
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
  });
}

function hexToBytes(value: string): Uint8Array {
  if (!/^[0-9a-f]{64}$/.test(value)) throw new Error('request signing key must be 32-byte lowercase hex');
  return Uint8Array.from(value.match(/.{2}/g)!, (byte) => Number.parseInt(byte, 16));
}

function bytesToHex(value: ArrayBuffer | Uint8Array): string {
  return Array.from(value instanceof Uint8Array ? value : new Uint8Array(value), (byte) => byte.toString(16).padStart(2, '0')).join('');
}

async function transaction<T>(mode: IDBTransactionMode, operation: (store: IDBObjectStore) => IDBRequest<T>): Promise<T> {
  const db = await openDatabase();
  return new Promise((resolve, reject) => {
    const tx = db.transaction(STORE_NAME, mode);
    const request = operation(tx.objectStore(STORE_NAME));
    request.onsuccess = () => resolve(request.result);
    request.onerror = () => reject(request.error);
    tx.oncomplete = () => db.close();
  });
}

export async function saveSigningSession(input: { sessionId: string; rawKeyHex: string; expiresAt: number }): Promise<void> {
  const key = await crypto.subtle.importKey('raw', hexToBytes(input.rawKeyHex), { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']);
  await transaction('readwrite', (store) => store.put({ sessionId: input.sessionId, key, expiresAt: input.expiresAt }));
}

export async function loadSigningSession(sessionId: string): Promise<{ key: CryptoKey; expiresAt: number } | null> {
  if (!sessionId) return null;
  const stored = await transaction<StoredSigningSession | undefined>('readonly', (store) => store.get(sessionId));
  if (!stored) return null;
  if (stored.expiresAt <= Math.floor(Date.now() / 1000)) {
    await transaction('readwrite', (store) => store.delete(sessionId));
    return null;
  }
  return { key: stored.key, expiresAt: stored.expiresAt };
}

export async function clearSigningSessions(): Promise<void> {
  await transaction('readwrite', (store) => store.clear());
}

export function createNonce(): string {
  return bytesToHex(crypto.getRandomValues(new Uint8Array(32)));
}

export async function canonicalRequest(input: { method: string; path: string; body: string; timestamp: number; nonce: string }): Promise<string> {
  const bodyHash = bytesToHex(await crypto.subtle.digest('SHA-256', new TextEncoder().encode(input.body)));
  return [VERSION, input.method.toUpperCase(), input.path, bodyHash, String(input.timestamp), input.nonce].join('\n');
}

export async function signRequest(key: CryptoKey, input: { method: string; path: string; body: string; timestamp: number; nonce: string }): Promise<string> {
  const canonical = await canonicalRequest(input);
  return bytesToHex(await crypto.subtle.sign('HMAC', key, new TextEncoder().encode(canonical)));
}
