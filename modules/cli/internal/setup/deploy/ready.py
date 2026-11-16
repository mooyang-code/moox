# 等一组组件就绪：有健康端口的请求本机 /readyz（200 才算），没有的（内置对象存储）看 systemctl --user is-active。
# 参数是一个 JSON：{"checks": [{"id": 组件, "port": 健康端口或 0, "unit": 单元名}], "timeout": 秒}。
# 输出一个 JSON：{"ready": [组件], "pending": [{"id": 组件, "detail": 原因}]}；到时限还有没就绪的也正常退出，由调用方判断。
import json, subprocess, sys, time, urllib.request

def probe(check):
    if check.get("port"):
        try:
            with urllib.request.urlopen("http://127.0.0.1:%d/readyz" % check["port"], timeout=3) as response:
                return "" if response.status == 200 else "readyz %d" % response.status
        except Exception as error:
            code = getattr(error, "code", None)
            return "readyz %s" % code if code else "健康端口 %d 连不上" % check["port"]
    try:
        state = subprocess.run(["systemctl", "--user", "is-active", check["unit"]], stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=10, text=True).stdout.strip()
    except (OSError, subprocess.SubprocessError) as error:
        return str(error)
    return "" if state == "active" else "服务状态 %s" % (state or "unknown")

request = json.loads(sys.argv[1])
pending = {check["id"]: check for check in request["checks"]}
details = {}
deadline = time.time() + float(request.get("timeout", 0))
while True:
    for name in list(pending):
        detail = probe(pending[name])
        if detail == "":
            del pending[name]
        else:
            details[name] = detail
    if not pending or time.time() >= deadline:
        break
    time.sleep(2)
ready = [check["id"] for check in request["checks"] if check["id"] not in pending]
print(json.dumps({"ready": ready, "pending": [{"id": name, "detail": details.get(name, "")} for name in pending]}))
