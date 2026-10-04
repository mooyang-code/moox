import fcntl
import json
import time


def compute(df, params, context):
    path = params["counter_path"]
    with open(path, "a+", encoding="utf-8") as handle:
        fcntl.flock(handle, fcntl.LOCK_EX)
        handle.seek(0)
        state = json.loads(handle.read() or '{"active":0,"maximum":0}')
        state["active"] += 1
        state["maximum"] = max(state["maximum"], state["active"])
        handle.seek(0)
        handle.truncate()
        handle.write(json.dumps(state))
        handle.flush()
        fcntl.flock(handle, fcntl.LOCK_UN)

    time.sleep(0.2)

    with open(path, "r+", encoding="utf-8") as handle:
        fcntl.flock(handle, fcntl.LOCK_EX)
        state = json.load(handle)
        state["active"] -= 1
        handle.seek(0)
        handle.truncate()
        handle.write(json.dumps(state))
        handle.flush()
        fcntl.flock(handle, fcntl.LOCK_UN)

    result = df[["data_time", "series_tag"]].copy()
    result["value_out"] = 1
    return result
