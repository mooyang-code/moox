#!/usr/bin/env python3
"""moox-trade 网关端到端验证。

前置：
  1. moox-trade 已启动（端口 11200-11208）
  2. moox-admin 网关已启动（端口 11000），且 admin DB 为 fresh 或可注册新用户

脚本流程：
  Register -> GetLoginSalt -> Login(AES-GCM 加密密码) ->
  trade_account/CreateAccount -> ListAccounts -> ListChannels -> ListApiKeys

依赖：requests, cryptography
"""
import requests, hashlib, os, base64, sys
from cryptography.hazmat.primitives.ciphers.aead import AESGCM

BASE = "http://127.0.0.1:11000/api/admin"
S = requests.Session()


def register(u, p):
    r = S.post(f"{BASE}/auth/Register",
               json={"app_info": {}, "username": u, "password": p, "nickname": u, "email": ""})
    print("Register:", r.status_code, r.text[:160])
    r.raise_for_status()


def salt(u):
    r = S.post(f"{BASE}/auth/GetLoginSalt", json={"username": u})
    r.raise_for_status()
    d = r.json()
    return d["salt"], int(d["timestamp"])


def login(u, p):
    s, ts = salt(u)
    key = hashlib.sha256((s + str(ts)).encode()).digest()
    iv = os.urandom(12)
    ct = AESGCM(key).encrypt(iv, p.encode(), None)
    ph = base64.b64encode(iv + ct).decode()
    body = {"app_info": {}, "username": u, "password_hash": ph, "salt": s, "timestamp": ts,
            "device_id": "py-e2e", "user_agent": "curl", "client_ip": "127.0.0.1"}
    r = S.post(f"{BASE}/auth/Login", json=body)
    d = r.json()
    print("Login:", r.status_code, "ret_info=", d.get("ret_info"))
    r.raise_for_status()
    return d.get("access_token") or d.get("token") or ""


def call(path, body, tok, space="sp_e2e"):
    h = {"Content-Type": "application/json", "X-Access-Token": tok, "X-Space-Id": space}
    r = S.post(f"{BASE}/{path}", json=body, headers=h)
    print(f"{path}: {r.status_code} {r.text[:200]}")
    r.raise_for_status()
    try:
        return r.json()
    except Exception:
        return {}


def main():
    u, p = "tradere2e", "TestPass123"
    register(u, p)
    tok = login(u, p)
    if not tok:
        print("E2E_FAIL: 无 token"); sys.exit(1)
    r = call("trade_account/CreateAccount",
             {"account_name": "e2e-spot", "account_type": "spot", "remark": "e2e"}, tok)
    acc_id = (r.get("account") or {}).get("account_id", "")
    call("trade_account/ListAccounts", {"page_no": 1, "page_size": 10}, tok)
    call("trade_channel/ListChannels", {"page_no": 1, "page_size": 10}, tok)
    call("trade_apikey/ListApiKeys",
         {"account_id": acc_id or "acc_x", "page_no": 1, "page_size": 10}, tok)
    print("E2E_OK")


if __name__ == "__main__":
    main()
