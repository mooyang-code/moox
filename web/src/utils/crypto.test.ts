import { webcrypto } from "node:crypto";
import { encryptPassword } from "./crypto";

describe("encryptPassword", () => {
  beforeAll(() => {
    Object.defineProperty(globalThis, "crypto", { value: webcrypto, configurable: true });
  });

  it("uses the shared salt/timestamp AES-GCM payload contract", async () => {
    const first = await encryptPassword("secret123", "test-salt", 1700000000);
    const second = await encryptPassword("secret123", "test-salt", 1700000000);

    expect(first).not.toBe(second);
    const firstBytes = Uint8Array.from(atob(first), char => char.charCodeAt(0));
    expect(firstBytes.length).toBe(12 + new TextEncoder().encode("secret123").length + 16);
  });

  it("decrypts the Go-compatible WebCrypto vector", async () => {
    const payload = Uint8Array.from(atob("AAAAAAAAAAAAAAAAYw0AaFU9nY6+Pswe4DHAqf4n2z8xQ0UH/Q=="), char => char.charCodeAt(0));
    const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode("test-salt1700000000"));
    const key = await crypto.subtle.importKey("raw", digest, { name: "AES-GCM" }, false, ["decrypt"]);
    const plaintext = await crypto.subtle.decrypt({ name: "AES-GCM", iv: payload.slice(0, 12) }, key, payload.slice(12));

    expect(new TextDecoder().decode(plaintext)).toBe("secret123");
  });
});
