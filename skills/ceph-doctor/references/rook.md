# Rook 层

## 先看 pod，再看日志

- `rook_pods` 给出 pod 状态、所在节点、重启次数与 waiting reason。
- 重启次数 ≥3 或 CrashLoopBackOff：`daemon_log daemon=<mon|osd|mgr|mds> id=<id> lines=200`
  看退出前的错误。
- 需要确认退出码时，`run_command` 执行 `kubectl -n <ns> get pod <pod> -o jsonpath=...`。

## 日志位置

- Rook 部署下日志通常在容器内，`read_log` 读不到；`daemon_log` 会自动选择宿主机或容器来源。
- `explore` 可以列出 `/var/lib/rook` 下的目录，确认实际布局。

## 判断顺序

pod 状态 → 重启次数与原因 → 该 daemon 的日志 → 对应 Ceph 的健康条目。
不要跳过 pod 状态直接翻日志。
