# Compactor — Detecting Issues & Tuning Strategy

Practical guidance for detecting per-tenant compaction issues and configuring an effective compaction strategy in a multi-tenant Mimir deployment.

## Part 1: Detecting Compaction Issues for a Specific Tenant

### Key Per-Tenant Metrics

The compactor exposes per-tenant metrics only through `BlocksCleaner` (the compaction loop itself tracks tenants as aggregate gauges that reset each cycle). Combine multiple signals for a complete picture.

#### Is compaction keeping up?

```promql
# Estimated pending compaction jobs per tenant — THE most useful per-tenant metric
# Labels: type="split" and type="merge"
cortex_bucket_index_estimated_compaction_jobs{user="TENANT_ID"}

# Alert: sustained rate of increase over 2h > 0 means compaction is falling behind
rate(cortex_bucket_index_estimated_compaction_jobs{user="TENANT_ID"}[2h]) > 0
```

#### Is the bucket index being updated?

```promql
# Last successful bucket index update per tenant
cortex_bucket_index_last_successful_update_timestamp_seconds{user="TENANT_ID"}

# Alert: if (now - this value) > 2 * compaction_interval, something is wrong
time() - cortex_bucket_index_last_successful_update_timestamp_seconds{user="TENANT_ID"} > 7200
```

#### Block count trends

```promql
# Total blocks per tenant (should stabilize, not keep growing indefinitely)
cortex_bucket_blocks_count{user="TENANT_ID"}

# Partial blocks — incomplete uploads, potential ingester issues
cortex_bucket_blocks_partials_count{user="TENANT_ID"}

# Blocks waiting for hard deletion (normal churn, but sustained growth = cleanup issue)
cortex_bucket_blocks_marked_for_deletion_count{user="TENANT_ID"}
```

### Global Compactor Metrics (Then Drill Into Tenant)

```promql
# Are compaction jobs failing?
rate(cortex_compactor_group_compactions_failures_total[5m]) > 0

# Are blocks getting stale? (histogram buckets: 1d to 5d in 12h steps)
# Growing values = compactor falling behind
cortex_compactor_block_max_time_delta_seconds

# Job duration by type — are split jobs taking too long?
histogram_quantile(0.99, rate(cortex_compactor_compaction_job_duration_seconds_bucket{type="split"}[1h]))
histogram_quantile(0.99, rate(cortex_compactor_compaction_job_duration_seconds_bucket{type="merge"}[1h]))

# Blocks auto-marked as unhealthy — corruption signal
cortex_compactor_blocks_marked_for_no_compaction_total{reason="out-of-order-chunks"}
cortex_compactor_blocks_marked_for_no_compaction_total{reason="critical"}

# Disk space issues
cortex_compactor_disk_out_of_space_errors_total > 0
```

### Log-Based Detection

Search for these patterns in compactor logs:

| Log Pattern | Severity | Meaning |
|-------------|----------|---------|
| `"failed to compact user blocks"` | ERROR | Tenant compaction failed after 3 retries |
| `"compaction job failed"` | ERROR | Specific job error — check `err=` field for root cause |
| `"max compaction time reached"` | INFO | 1h budget exhausted before all jobs finished for this tenant |
| `"skipped compaction because job is not owned"` | INFO | Ring instability — jobs being reshuffled between compactors |
| `"compacted blocks verification failed"` | WARN | Output block has wrong time range — potential data integrity issue |
| `"failed to mark for deletion an empty block"` | WARN | Zero-sample blocks appearing during compaction |

### Diagnosing a Specific Stuck Tenant

Follow this investigation flow:

1. **Check pending jobs**: `cortex_bucket_index_estimated_compaction_jobs{user="TENANT", type=~"split|merge"}`
   - Are jobs piling up? Which type — split or merge?

2. **If split jobs pile up** — the split stage is bottlenecked:
   - Too few `splitGroups` for the tenant's block volume
   - Split job hitting `MaxCompactionTime` (default 1h) before completing
   - Disk space exhaustion during split (split writes `shardCount` output blocks simultaneously)
   - A corrupted source block causing repeated split failures

3. **If merge jobs pile up** — two possible causes:
   - Split jobs haven't completed yet (merge is blocked by the conflict detection rule — see below)
   - Merge is itself slow (too many blocks per shard, large 24h merges)

4. **Check for corrupted blocks**: `cortex_compactor_blocks_marked_for_no_compaction_total`
   - No-compact markers create "holes" that prevent higher-level merges

5. **Check for partial blocks**: `cortex_bucket_blocks_partials_count{user="TENANT"}`
   - Partial blocks may indicate ingester upload issues

### Critical: Split-Blocks-Merge Dependency

Split jobs **block ALL merge jobs for overlapping time ranges** (enforced in `split_merge_job.go:49`). If a tenant's 2h split job gets stuck or keeps failing, it prevents 12h and 24h merges from ever being planned. This cascading block is the number one cause of "compaction falling behind" for large tenants.

---

## Part 2: Compaction Strategy for Multi-Tenant Environments

### Understanding the Three Knobs

| Setting | What It Controls | Scope |
|---------|-----------------|-------|
| `compactor_split_and_merge_shards` | Number of **output shards** per split job. Each shard contains `~totalSeries / shardCount` series. Also used by query frontend for query parallelism alignment. | Per-tenant |
| `compactor_split_groups` | Number of **concurrent split jobs** per time window. Source blocks are distributed across jobs via `HashBlockID(ULID) % splitGroups`. | Per-tenant |
| `compactor_tenant_shard_size` | Number of **compactor instances** in this tenant's shuffle shard. Jobs are distributed across these instances via ring hashing. | Per-tenant |

Key relationships:
- `split_groups` controls **input-side parallelism** (how many concurrent split jobs)
- `split_and_merge_shards` controls **output sharding** (how many output blocks per split)
- `tenant_shard_size` controls **infrastructure parallelism** (how many compactor pods work on this tenant)
- **`split_groups` should be ≤ `tenant_shard_size`** so jobs actually distribute across instances

### When to Enable Splitting (`split_and_merge_shards > 0`)

Enable splitting when:
- A tenant has more than ~1-2M active series
- 24h block compaction takes too long or consumes too much memory
- You want query parallelism alignment (query frontend uses the shard count)

Keep splitting disabled (`split_and_merge_shards = 0`) when:
- Tenant has fewer than ~1M active series — splitting overhead not justified
- The additional object storage operations and block churn are undesirable

### Sizing Guide

**Target**: keep per-shard 24h block size manageable at approximately 1-3M series per shard.

| Tenant Size | Active Series | `split_and_merge_shards` | `split_groups` | `tenant_shard_size` |
|------------|--------------|--------------------------|----------------|---------------------|
| Small | < 1M | `0` (disabled) | `1` | `4` |
| Medium | 1-5M | `4` | `2` | `4` |
| Large | 5-20M | `8-16` | `4` | `4-8` |
| Very Large | > 20M | `16-32` | `4-8` | `8+` |

### Example: Configuring a 20M Active Series Tenant

With 20M active series and `split_and_merge_shards: 4`, each shard holds ~5M series. That is a very large block — TSDB compaction for 5M series in a 24h range is CPU/memory intensive and slow.

**Recommended:**

```yaml
# Per-tenant overrides
compactor_split_and_merge_shards: 16   # each shard ~ 1.25M series
compactor_split_groups: 4              # 4 concurrent split jobs (matches tenant_shard_size)
compactor_tenant_shard_size: 4         # 4 compactor instances for this tenant
```

If split jobs still take too long or hit `MaxCompactionTime`:

```yaml
compactor_split_groups: 8              # more split parallelism
compactor_tenant_shard_size: 8         # more compactors to distribute the load
```

### Default Settings for Small Tenants

```yaml
compactor_tenant_shard_size: 4         # limits blast radius per tenant
compactor_split_groups: 1              # no split parallelism needed
compactor_split_and_merge_shards: 0    # splitting disabled, pure merge
```

### Global Compactor Tuning

Settings worth reviewing beyond per-tenant overrides:

```yaml
# Job ordering — if compaction is lagging and you want recent data merged first:
-compactor.compaction-jobs-order: newest-blocks-first
# Default (smallest-range-oldest-blocks-first) is better when caught up

# Increase if split jobs with many shards cause ENOSPC
# Each split job writes shardCount output files simultaneously
-compactor.max-closing-blocks-concurrency: 2  # default 1; increase for fast SSDs
-compactor.symbols-flushers-concurrency: 2     # default 1; increase for large blocks

# In-memory meta cache — reduces object storage calls for big tenants with many blocks
-compactor.in-memory-tenant-meta-cache-size: 512

# Compaction concurrency — parallel workers per tenant (default 1)
# Increase only if the compactor has enough CPU/memory/disk headroom
-compactor.compaction-concurrency: 2
```

### What Happens When You Change `split_and_merge_shards` on an Existing Tenant

- **Increasing shards**: Historical blocks already split with the old shard count keep their `__compactor_shard_id__` labels. New first-level blocks are split with the new count. The grouper handles this naturally since each unique shard ID gets its own merge track. However, historical 12h/24h blocks that were already merged at the old shard count will NOT be re-split — only new first-level (2h) blocks go through the split stage.

- **Decreasing shards**: Same behavior — old shards remain, new blocks use the new count. Over time (as old blocks age out via retention), the system converges to the new shard count.

- **Enabling splitting on a previously unsharded tenant** (`0 → N`): Only first-level blocks without `__compactor_shard_id__` go through split. Large historical unsharded blocks at 12h/24h ranges are left as-is — the compactor does not re-split them.

### Monitoring Your Strategy

After changing compaction settings, watch these metrics over 24-48 hours:

```promql
# Pending jobs should decrease or stabilize (not keep growing)
cortex_bucket_index_estimated_compaction_jobs{user="TENANT"}

# Job duration should be reasonable (< 30m for split, < 15m for merge)
histogram_quantile(0.95, rate(cortex_compactor_compaction_job_duration_seconds_bucket[1h]))

# Block count should stabilize after initial churn
cortex_bucket_blocks_count{user="TENANT"}

# No disk space issues
cortex_compactor_disk_out_of_space_errors_total

# Compaction delay should decrease (blocks being compacted sooner after upload)
cortex_compactor_block_compaction_delay_seconds
```
