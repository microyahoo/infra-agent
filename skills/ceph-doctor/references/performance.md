# 性能

## slow ops / 阻塞请求

1. `slow_ops` 找出被阻塞的 OSD 与请求年龄。
2. 这些 OSD 若同时出现在 down / 慢心跳列表里，根因就是它们，不必另查。
3. 否则对每个 OSD：`perf_counters osd_id=<id>` 看 op latency 与队列深度，
   再 `host_io node=<节点> device=<盘>` 看磁盘 util / await。

## 延迟高但无 slow ops

- `perf_counters` 对比各 OSD 的 apply / commit latency，找出离群值。
- `host_check` 检查 CPU steal、内存压力、网络重传。
- 单块盘慢会拖累整个池；定位到盘后，问题就变成"替换或隔离这块盘"。

## 不要做的事

- 不要在未定位到具体 OSD 前对所有 OSD 跑 `perf_counters`。
- 不要用 `run_command` 执行 `ceph osd perf` 之类的重复查询——`perf_counters` 已经覆盖。
