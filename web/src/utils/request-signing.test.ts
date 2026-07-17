import "fake-indexeddb/auto";
import { afterEach, describe, expect, it } from "vitest";
import {
  canonicalRequest,
  clearSigningSessions,
  createNonce,
  loadSigningSession,
  saveSigningSession,
  signRequest
} from "./request-signing";

const rawKeyHex = "00".repeat(32);
const nonce = "0123456789abcdef".repeat(4);

afterEach(async () => clearSigningSessions());

describe("signing session storage", () => {
  it("persists a non-exportable HMAC key by session id without persisting raw key material", async () => {
    await saveSigningSession({ sessionId: "sid-1", rawKeyHex, expiresAt: 2_000_000_000 });

    const session = await loadSigningSession("sid-1");
    expect(session?.expiresAt).toBe(2_000_000_000);
    expect(session?.key.extractable).toBe(false);
    await expect(crypto.subtle.exportKey("raw", session!.key)).rejects.toThrow();
  });

  it("deletes expired sessions when loading them", async () => {
    await saveSigningSession({ sessionId: "expired", rawKeyHex, expiresAt: 1 });
    expect(await loadSigningSession("expired")).toBeNull();
  });
});

describe("request signing", () => {
  it("matches the Go canonical request test vector", async () => {
    expect(
      await canonicalRequest({
        method: "post",
        path: "/api/admin/items/a%2Fb",
        body: '{"a":1}\n',
        timestamp: 1_700_000_000,
        nonce
      })
    ).toBe(
      [
        "moox-request-v1",
        "POST",
        "/api/admin/items/a%2Fb",
        "e346432021b04179518d9614f3560ccd71354a4ee101ddcb893d6959a9d6301c",
        "1700000000",
        nonce
      ].join("\n")
    );
  });

  it("creates a 64 character lowercase hexadecimal nonce", () => {
    expect(createNonce()).toMatch(/^[0-9a-f]{64}$/);
  });

  it("signs the exact serialized request body", async () => {
    await saveSigningSession({ sessionId: "sid-1", rawKeyHex, expiresAt: 2_000_000_000 });
    const key = (await loadSigningSession("sid-1"))!.key;
    const first = await signRequest(key, { method: "POST", path: "/api/admin/x", body: '{"a":1}', timestamp: 1, nonce });
    const second = await signRequest(key, { method: "POST", path: "/api/admin/x", body: '{ "a": 1 }', timestamp: 1, nonce });
    expect(first).toMatch(/^[0-9a-f]{64}$/);
    expect(second).not.toBe(first);
  });

  it("uses the same hexadecimal signing-key bytes as the Go verifier", async () => {
    await saveSigningSession({ sessionId: "sid-vector", rawKeyHex, expiresAt: 2_000_000_000 });
    const key = (await loadSigningSession("sid-vector"))!.key;
    const signature = await signRequest(key, {
      method: "POST",
      path: "/api/admin/items/a%2Fb",
      body: '{"a":1}\n',
      timestamp: 1_700_000_000,
      nonce
    });

    expect(signature).toBe("e4d0467d78c431898b10a666fbe6fc68a677d810e4dfeeba77ece7ffe8f29ef8");
  });

  it("binds tenant and application headers in a stable order", async () => {
    const canonical = await canonicalRequest({
      method: "post",
      path: "/api/admin/x",
      body: "{}",
      timestamp: 1_700_000_000,
      nonce,
      headers: {
        "x-space-id": " space-1 ",
        "X-App-Id": "frontend"
      }
    });
    expect(canonical).toContain("x-app-id:frontend\nx-app-key:\nx-space-id:space-1");
  });
});
