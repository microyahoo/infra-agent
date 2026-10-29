# PG 与容量

## 异常 PG

- `pg_detail` 返回各状态数量与异常 PG 列表。先看状态分布，不要逐条读。
- active+undersized / degraded / peering 同时出现且伴随 OSD down：与 OSD 问题是同一根因，
  OSD 恢复后 PG 会自动收敛，不要单独处理 PG。
- 只有 PG 异常、没有 OSD down：查 `pool_info` 的副本数与 `osd_tree` 的可用 OSD 数是否匹配。

## 容量

- `pool_info` 给出各池的容量与使用率。容量告警时直接找使用率最高的池。
- 需要确认物理容量时用 `ceph_status` 的 bytes_total / bytes_used。
- 不要逐个 PG 查容量；池级数据足够回答"哪个池占满"。

## 不要做的事

- 不要试图通过重复查询等待 PG 恢复；用一次 `pg_detail` 记录当下状态即可。
