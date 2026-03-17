# Compactor — Operational Deep Dive

This document provides a comprehensive analysis of the Mimir compactor implementation for SRE operational use. The primary focus is understanding why compaction can get stuck and how to avoid that in a multi-tenant setup.

## Architecture Overview

The compactor is a layered system with three main tiers:

```
MultitenantCompactor (compactor.go)
  ├── Ring-based tenant sharding (compactor_ring.go)
  ├── Tenant discovery + iteration loop
  │
  ├── BucketCompactor (bucket_compactor.go) — one per tenant per cycle
  │     ├── metaSyncer — fetches block metadata from object storage
  │     ├── Grouper (split_merge_grouper.go) — plans compaction jobs
  │     ├── Planner — decides which blocks within a job to actually compact
  │     └── Compactor (split_merge_compactor.go) — runs TSDB LeveledCompactor
  │
  └── BlocksCleaner (blocks_cleaner.go) — runs on separate timer
        ├── Bucket index updates
        ├── Soft-delete → hard-delete lifecycle
        └── Partial block cleanup
```

### Key Design Choices
- **Stateless compactors**: All state lives in object storage. Compactors download blocks, compact locally, upload results, then clean up. The local `data-dir` is ephemeral.
- **Split-and-merge model**: First-level blocks are *split* into N shards by series hash, then each shard is *merged* independently. This enables horizontal scaling of compaction work across compactor instances.
- **Job-level sharding**: All compactors in a tenant's shard plan all jobs, but each job is owned by exactly one compactor (determined by hashing the job's sharding key against the ring).

## Multi-Tenant Sharding Model

### Ring Configuration
- **Tokens per instance**: 512 (hardcoded in `compactor_ring.go:24`, not configurable)
- **Replication factor**: 1 (each job has exactly one owner)
- **`KeepInstanceInTheRingOnShutdown`**: `false` — compactors deregister on graceful shutdown
- **Wait stability**: min=0, max=5m — compactor waits for ring to stabilize at startup
- **Wait active timeout**: 10m — time to wait for instance to become ACTIVE
- **Auto-forget unhealthy**: after 10 consecutive missed heartbeat periods

### Tenant-to-Compactor Assignment (splitAndMergeShardingStrategy)

There are two levels of ownership:

1. **Tenant ownership** (`compactorOwnsUser`): Uses shuffle shard. All compactors in a tenant's shuffle shard own the tenant — meaning they all plan jobs for that tenant. Controlled by `CompactorTenantShardSize` (0 = use all compactors).

2. **Job ownership** (`ownJob`): Within the tenant's shuffle shard, each specific compaction job is owned by exactly one compactor. The job's `shardingKey` (format: `{userID}-{stage}-{rangeStart}-{rangeEnd}-{shardID}`) is hashed via FNV-32a and mapped to a ring token to determine the owner.

3. **Cleanup ownership** (`blocksCleanerOwnsUser`): Only a single compactor in the tenant's shard runs cleanup/bucket-index updates — determined by hashing the tenant ID against the ring.

### Important implication
Job ownership is checked **twice**: once when filtering planned jobs (`filterOwnJobs` at `bucket_compactor.go:1173`), and again just before execution in the worker goroutine (`bucket_compactor.go:978`). This double-check prevents stale ownership from causing duplicate compactions after ring changes.

## Compaction Lifecycle

### 1. Main Loop (`running()` — compactor.go:601)
```
Start → compactUsers() → wait CompactionInterval (1h default, ±5% jitter) → repeat
```

### 2. Tenant Discovery and Iteration (`compactUsers()` — compactor.go:620)
1. List all tenant directories from object storage (`ListUsers`)
2. Shuffle randomly (reduces thundering herd when multiple compactors start simultaneously)
3. For each tenant:
   - Check shard ownership (`compactorOwnsUser`)
   - Check tenant deletion mark
   - Call `compactUserWithRetries()`
4. After all tenants: clean up local directories for unowned tenants

### 3. Per-Tenant Compaction (`compactUser()` — compactor.go:769)
1. Create user-scoped bucket client
2. Set up metadata filters:
   - `LabelRemoverFilter` — strips ingester/tenant ID labels
   - `ShardAwareDeduplicateFilter` — removes duplicate blocks
   - `NoCompactionMarkFilter` — excludes blocks with no-compact markers
3. Create `BucketCompactor` with per-tenant settings
4. Call `BucketCompactor.Compact()`

### 4. BucketCompactor.Compact() Loop (bucket_compactor.go:937)
This runs in a **loop** until no more work remains:
1. **Sync metas** — fetch block metadata from object storage
2. **Garbage collect** — mark deletion for blocks whose sources are fully compacted
3. **Plan jobs** — `grouper.Groups()` returns all compaction jobs
4. **Filter own jobs** — keep only jobs owned by this compactor instance
5. **Filter by wait period** — skip first-level jobs with recently uploaded blocks
6. **Sort jobs** — by configured ordering (default: `smallest-range-oldest-blocks-first`)
7. **Start max compaction timer** — after first planning completes (not before!)
8. **Dispatch to workers** — send jobs to `concurrency` worker goroutines (default: 1)
9. **Worker execution**:
   - Re-check job ownership (ring may have changed)
   - Call `runCompactionJob()` — plan → download → compact → upload
   - Handle unhealthy blocks: mark no-compact if `skipUnhealthyBlocks` is enabled
10. If any job signaled `shouldRerun` (e.g., after marking no-compact), loop again
11. If `maxCompactionTime` reached or all jobs finished, exit loop

### 5. Retry Logic (`compactUserWithRetries()` — compactor.go:748)
- Default: 3 retries with exponential backoff (10s min, 60s max)
- Retries the entire `compactUser()` call — including metadata sync
- Context cancellation (shutdown) short-circuits immediately

## Split-and-Merge Compaction Model

### Block Ranges (default: 2h, 12h, 24h)
Each range period must be divisible by the previous one. Blocks are progressively compacted through these ranges. The compactor warns if the largest range isn't 24h, as the read path assumes 24h blocks for cache TTLs and query splitting.

### Two Stages

**Split stage** (first range only):
- Takes non-sharded level-1 blocks uploaded by ingesters
- Groups them by hash of block ULID into `splitGroups` groups
- Each group is split into `shardCount` output blocks based on series hash
- Output blocks get a `__compactor_shard_id__` external label

**Merge stage** (all ranges):
- Takes sharded blocks within the same time range and shard
- Merges 2+ blocks into a single compacted block
- Applies across all compaction ranges (2h→12h→24h)

### Conflict Detection (`job.conflicts()` — split_merge_job.go:49)
Two jobs conflict if they:
- Belong to the same user
- Have overlapping time ranges
- Have the same external labels (excluding shard ID)
- Are either in different stages (split blocks first, then merge), OR in the same stage and same shard

This is critical: **split jobs block merge jobs for overlapping time ranges**. If splitting gets stuck, merging for that range also stalls.

### Wait Period (`jobWaitPeriodElapsed()` — job.go:160)
- Default: 25 minutes (`-compactor.first-level-compaction-wait-period`)
- Applies only to level-1 (first compaction) blocks
- Checks the `LastModified` timestamp of each block's meta.json in object storage
- Skips out-of-order blocks
- Purpose: wait for all ingesters to upload their blocks before compacting, reducing partial compactions

## Per-Tenant Issue Detection & Tuning Strategy

See [COMPACT_ADVISES.md](COMPACT_ADVISES.md) for detailed guidance on:
- Detecting compaction issues for a specific tenant (metrics, logs, investigation flow)
- Sizing `split_and_merge_shards`, `split_groups`, and `tenant_shard_size` for multi-tenant environments
- A decision matrix for small/medium/large/very-large tenants

### Quick Sizing Reference

| Tenant Size | Active Series | `shards` | `split_groups` | `shard_size` |
|------------|--------------|----------|----------------|--------------|
| Small | < 1M | `0` | `1` | `4` |
| Medium | 1-5M | `4` | `2` | `4` |
| Large | 5-20M | `8-16` | `4` | `4-8` |
| Very Large | > 20M | `16-32` | `4-8` | `8+` |

Target: ~1-3M series per shard in 24h blocks. Keep `split_groups ≤ tenant_shard_size`.

## Stuck Compaction Scenarios

### Scenario 1: Unhealthy / Corrupted Block Blocking a Job

**Symptom**: `cortex_compactor_group_compactions_failures_total` increasing; same job fails repeatedly; logs show "block with unhealthy index found" or "out-of-order chunks".

**Cause**: A block with corrupted data, out-of-order chunks, or invalid index prevents the entire compaction job from completing. Since that block's time range can't be compacted, higher-level compactions that depend on it also stall.

**Fix**:
- Ensure `-compactor.skip-blocks-with-out-of-order-chunks-enabled` is true (this is the `skipUnhealthyBlocks` field). When enabled, the compactor automatically marks unhealthy blocks with a no-compact marker and retries without them.
- If not enabled, manually upload a `no-compact-mark.json` for the problematic block.
- Check `cortex_compactor_blocks_marked_for_no_compaction_total` to see if auto-marking is working.

### Scenario 2: Split Stage Blocking Merge Stage

**Symptom**: Merge jobs for a time range never execute even though split jobs are planned. `cortex_compactor_block_max_time_delta_seconds` grows as blocks age without being merged.

**Cause**: The conflict detection logic (`job.conflicts()`) prevents merge jobs from running when split jobs exist for the same time range. If splitting fails or is slow, merging is completely blocked.

**Fix**:
- Investigate split job failures first — they're the root cause
- Increase `CompactorSplitGroups` to create more but smaller split jobs (reduces per-job blast radius)
- If a specific block is causing split failures, mark it no-compact

### Scenario 3: Single Tenant Starving Others (MaxCompactionTime)

**Symptom**: Only one or a few tenants get compacted per cycle. Other tenants' `cortex_compactor_block_max_time_delta_seconds` grows continuously.

**Cause**: Tenants are processed sequentially in `compactUsers()`. A tenant with many blocks can consume the entire compaction interval. `BucketCompactor.Compact()` loops until all jobs are done or `maxCompactionTime` is reached.

**Fix**:
- Set `-compactor.max-compaction-time` (default: 1h) to limit per-tenant time. After this duration, no new jobs start (in-flight jobs complete normally).
- Use `-compactor.compaction-jobs-order` to prioritize (`smallest-range-oldest-blocks-first` is the default and usually best).
- Increase `CompactorTenantShardSize` so the large tenant spreads across more compactors.

### Scenario 4: Ring Instability Causing Job Ownership Churn

**Symptom**: Logs show "skipped compaction because job is not owned by the compactor instance anymore". Compactions start but don't complete. Multiple compactors log conflicting ownership.

**Cause**: When compactor instances join/leave the ring, job ownership shifts. The double-check (plan time + execution time) will skip jobs that moved. During ring instability (scaling events, restarts), many jobs can be skipped.

**Fix**:
- Increase `-compactor.ring.wait-stability-min-duration` (default: 0) so compactors wait for the ring to settle before starting
- Avoid frequent compactor scaling events
- Use `-compactor.ring.wait-stability-max-duration` (default: 5m) as an upper bound

### Scenario 5: Disk Space Exhaustion (ENOSPC)

**Symptom**: `cortex_compactor_disk_out_of_space_errors_total` increases. Compaction fails with ENOSPC errors.

**Cause**: Compaction downloads all source blocks locally, then writes a compacted output. For large tenants, this can require significant disk space. Split compaction with many shards multiplies the output size.

**Fix**:
- Increase compactor disk size (the data dir is ephemeral, so fast local storage is ideal)
- Reduce `-compactor.compaction-concurrency` (default: 1) to limit parallel disk usage
- Reduce `-compactor.max-closing-blocks-concurrency` (default: 1) to limit memory/disk during split output
- The compactor cleans up the work directory after successful compaction, but failed compactions leave data behind until the next cycle

### Scenario 6: Object Storage Throttling / Errors

**Symptom**: Meta sync failures ("failed to discover users from bucket"), block download/upload errors, compaction retries exhausted.

**Cause**: Object storage rate limits, transient errors, or connectivity issues. The compactor makes many API calls: listing tenants, syncing metadata, downloading blocks, uploading results.

**Fix**:
- Check `-compactor.block-sync-concurrency` (default: 8) and `-compactor.meta-sync-concurrency` (default: 20) — reduce if hitting rate limits
- Retry logic (3 retries, 10s-60s backoff) handles transient issues
- Monitor `thanos_objstore_bucket_operation_failures_total`

### Scenario 7: Wait Period Preventing Compaction

**Symptom**: First-level blocks accumulate but never compact. No compaction errors — jobs are simply filtered out.

**Cause**: The 25-minute wait period checks the `LastModified` time of `meta.json` in object storage. If block upload is delayed (slow ingesters, clock skew), or if the wait period is too long, blocks are perpetually "too new" to compact.

**Fix**:
- Check ingester upload health and timing
- Reduce `-compactor.first-level-compaction-wait-period` (default: 25m) if appropriate
- Note: the wait period intentionally does NOT apply to out-of-order blocks (`meta.OutOfOrder` flag)

### Scenario 8: No-Compact Markers Accumulating

**Symptom**: Blocks marked with no-compact accumulate. Overall compaction works but specific time ranges have fragmented blocks that can't be merged.

**Cause**: Once a block is marked no-compact, it's excluded from compaction forever. If many blocks in a time range are marked, the compactor can't produce the expected 24h blocks, degrading query performance.

**Fix**:
- Review no-compact markers: look for `no-compact-mark.json` files in block directories
- If the underlying issue is fixed (e.g., block was re-uploaded correctly), manually remove the no-compact marker
- Monitor `cortex_compactor_blocks_marked_for_no_compaction_total` by reason label

### Scenario 9: Compactor Not Owning Any Tenants After Restart

**Symptom**: Compactor starts, logs "discovering users from bucket", but all tenants are skipped. `cortex_compactor_tenants_skipped` equals `cortex_compactor_tenants_discovered`.

**Cause**: The compactor instance is in the ring but hasn't received any tokens, or the ring hasn't stabilized. `KeepInstanceInTheRingOnShutdown` is false, so after restart the instance re-registers with new tokens.

**Fix**:
- Wait for `-compactor.ring.wait-active-instance-timeout` (default: 10m)
- Check ring UI/API to verify the instance is ACTIVE with 512 tokens
- Verify KV store connectivity (`-compactor.ring.` consul/etcd/memberlist settings)

## BlocksCleaner

The `BlocksCleaner` runs on a separate timer (`-compactor.cleanup-interval`, default: 15m) with its own concurrency (`-compactor.cleanup-concurrency`, default: 20 tenants in parallel).

### What it does:
1. **Updates bucket index** — the index of all blocks for a tenant, used by store-gateways
2. **Soft-delete → hard-delete**: Blocks marked for deletion are only hard-deleted after `-compactor.deletion-delay` (default: 12h). This gives store-gateways time to stop serving them.
3. **Partial block cleanup**: Blocks without a complete `meta.json` (e.g., from a crashed upload) are cleaned up after `CompactorPartialBlockDeletionDelay`.
4. **Tenant cleanup**: For deleted tenants, waits `-compactor.tenant-cleanup-delay` (default: 6h) after last block is gone before removing marker/debug files.

### Cleanup ownership
Only one compactor per tenant shard runs cleanup (via `blocksCleanerOwnsUser`, which uses ring-based token ownership — unlike `compactorOwnsUser` where all shard members own).

## Global Markers (Dual-Write Pattern)

Markers (deletion marks, no-compact marks) are stored in two locations:
1. **Per-block**: `{tenant}/{blockID}/deletion-mark.json` or `no-compact-mark.json`
2. **Global**: `{tenant}/markers/{blockID}-deletion-mark.json`

The `globalMarkersBucket` wrapper (`global_markers_bucket_client.go`) intercepts `Upload` and `Delete` calls to maintain both locations. The global location enables efficient listing of all markers without iterating every block directory.

## Key Metrics for Monitoring

### Compaction Health
| Metric | What to watch |
|--------|--------------|
| `cortex_compactor_runs_started_total` | Should increase by 1 every compaction interval |
| `cortex_compactor_runs_completed_total` | Should track `runs_started` closely |
| `cortex_compactor_runs_failed_total{reason="error"}` | Any sustained increase needs investigation |
| `cortex_compactor_runs_failed_total{reason="shutdown"}` | Expected during rolling restarts |
| `cortex_compactor_last_successful_run_timestamp_seconds` | Alert if too old (> 2× compaction interval) |

### Per-Tenant Progress
| Metric | What to watch |
|--------|--------------|
| `cortex_compactor_tenants_discovered` | Total tenants in bucket |
| `cortex_compactor_tenants_skipped` | Tenants not owned + tenants marked for deletion |
| `cortex_compactor_tenants_processing_succeeded` | Should be close to (discovered - skipped) |
| `cortex_compactor_tenants_processing_failed` | Any non-zero needs investigation |

### Job-Level Metrics
| Metric | What to watch |
|--------|--------------|
| `cortex_compactor_group_compaction_runs_started_total` | Jobs dispatched to workers |
| `cortex_compactor_group_compaction_runs_completed_total` | Should track started |
| `cortex_compactor_group_compactions_failures_total` | Persistent failures = stuck compaction |
| `cortex_compactor_compaction_job_duration_seconds` | By label `job_type` (split/merge) |
| `cortex_compactor_block_max_time_delta_seconds` | Growing = compactor falling behind |

### Block Health
| Metric | What to watch |
|--------|--------------|
| `cortex_compactor_blocks_marked_for_deletion_total` | Normal compaction byproduct |
| `cortex_compactor_blocks_marked_for_no_compaction_total` | Watch by reason label |
| `cortex_compactor_disk_out_of_space_errors_total` | Needs disk resize |

## Configuration Reference

### Core Compaction
| Flag | Default | Description |
|------|---------|-------------|
| `-compactor.block-ranges` | `2h,12h,24h` | Compaction time ranges. Each must be divisible by previous. |
| `-compactor.compaction-interval` | `1h` | How often the compaction loop runs |
| `-compactor.compaction-retries` | `3` | Retries per tenant per cycle |
| `-compactor.compaction-concurrency` | `1` | Parallel compaction workers per tenant |
| `-compactor.max-compaction-time` | `1h` | Max time for starting new compactions for a single tenant (0=disabled) |
| `-compactor.compaction-jobs-order` | `smallest-range-oldest-blocks-first` | Job sorting algorithm |
| `-compactor.first-level-compaction-wait-period` | `25m` | Wait for ingesters to upload before compacting level-1 blocks |
| `-compactor.data-dir` | `./data-compactor/` | Ephemeral local directory for compaction work |

### Concurrency Tuning
| Flag | Default | Description |
|------|---------|-------------|
| `-compactor.block-sync-concurrency` | `8` | Parallel block downloads/uploads |
| `-compactor.meta-sync-concurrency` | `20` | Parallel metadata syncs |
| `-compactor.max-opening-blocks-concurrency` | `1` | Parallel block opens before compaction |
| `-compactor.max-closing-blocks-concurrency` | `1` | Parallel block closes during split (memory-intensive) |
| `-compactor.symbols-flushers-concurrency` | `1` | Symbol flushers during split compaction |

### Cleanup
| Flag | Default | Description |
|------|---------|-------------|
| `-compactor.cleanup-interval` | `15m` | How often cleanup runs |
| `-compactor.cleanup-concurrency` | `20` | Parallel tenant cleanups |
| `-compactor.deletion-delay` | `12h` | Time between soft-delete and hard-delete |
| `-compactor.tenant-cleanup-delay` | `6h` | Delay before final tenant cleanup after last block removed |

### Ring / Sharding
| Flag | Default | Description |
|------|---------|-------------|
| `-compactor.ring.wait-stability-min-duration` | `0` | Min time to wait for ring to stabilize at startup |
| `-compactor.ring.wait-stability-max-duration` | `5m` | Max time to wait for ring stability |
| `-compactor.ring.wait-active-instance-timeout` | `10m` | Time to wait for ACTIVE state |
| `-compactor.ring.auto-forget-unhealthy-periods` | `10` | Auto-forget after N missed heartbeats |

### Tenant Filtering
| Flag | Default | Description |
|------|---------|-------------|
| `-compactor.enabled-tenants` | (empty) | Allowlist of tenants (subject to sharding) |
| `-compactor.disabled-tenants` | (empty) | Blocklist of tenants (overrides sharding) |
