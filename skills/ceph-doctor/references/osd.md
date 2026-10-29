# OSD 故障

## OSD down

1. `ceph_overview` 已给出 down 的 OSD 及其节点。先看是否多个 OSD 落在同一节点。
2. 同一节点 ≥2 个 OSD down：先 `host_check node=<节点>` 确认节点是否可达、负载是否异常。
   节点不可达时不要逐个查 OSD。
3. 单个 OSD down：`osd_detail osd_id=<id>` 看状态与设备，再 `host_io node=<节点> device=<盘>`
   看磁盘是否报错。需要时用 `read_log` 读该 OSD 的日志。

## 慢心跳

- `ceph_health` 的 Slow OSD heartbeats 条目会给出成对 OSD。
- 对反复出现的 OSD，查其所在节点的 `host_check`（负载、网络）与 `host_io`（磁盘延迟）。
- 如果慢心跳对象正是 down 的那批 OSD，它们是同一件事，不要拆成两个问题。

## 判断顺序

节点可达性 → 磁盘健康 → OSD 进程状态 → 网络。先用 `host_check` / `host_io` 排除硬件与节点，
再进 `osd_detail`，最后才看 `daemon_log`。
