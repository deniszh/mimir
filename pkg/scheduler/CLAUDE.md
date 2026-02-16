# Query Scheduler Queue Architecture

## Overview

The query scheduler (`pkg/scheduler/`) is responsible for accepting query requests from query-frontends and dispatching them to querier-workers. The core queueing logic lives in a `RequestQueue` which uses a two-level `MultiAlgorithmTreeQueue` to make scheduling decisions.

The tree structure enables two independent scheduling algorithms to operate in a defined hierarchy:

1. **Level 1 (root)**: Query component selection — partitions querier-workers across query component subtrees to isolate latency effects between ingesters and store-gateways.
2. **Level 2**: Tenant selection — round-robin across tenants with optional shuffle-sharding enforcement.
3. **Leaf nodes**: Per-tenant FIFO queues within each query component partition.

```
Root (QuerierWorkerQueuePriorityAlgo)
├── ingester (TenantQuerierQueuingAlgorithm)
│   ├── tenant-1 [FIFO queue]
│   └── tenant-2 [FIFO queue]
├── store-gateway
│   ├── tenant-1 [FIFO queue]
│   └── tenant-3 [FIFO queue]
├── ingester-and-store-gateway
│   └── tenant-2 [FIFO queue]
└── unknown
    └── tenant-1 [FIFO queue]
```

## Request Flow

### Enqueue path

1. `Scheduler.FrontendLoop` receives a gRPC stream from a query-frontend.
2. `Scheduler.enqueueRequest` extracts the tenant ID, builds a `SchedulerRequest` with `AdditionalQueueDimensions` (the expected query component, set by the frontend based on the query's time range).
3. `RequestQueue.SubmitRequestToEnqueue` sends the request to the single-threaded `dispatcherLoop` via channel.
4. `queueBroker.enqueueRequestBack` builds a `QueuePath` of `[queryComponent, tenantID]`, checks the per-tenant queue depth limit (`maxTenantQueueSize`), and inserts into the tree.

### Dequeue path

1. `Scheduler.QuerierLoop` receives a gRPC stream from a querier-worker. The querier-worker registers itself and submits `QuerierWorkerDequeueRequest`s.
2. The `dispatcherLoop` processes dequeue requests: `queueBroker.dequeueRequestForQuerier` calls `tree.Dequeue` with `DequeueArgs{QuerierID, WorkerID, LastTenantIndex}`.
3. `QuerierWorkerQueuePriorityAlgo` selects a query component subtree based on `WorkerID % len(nodeOrder)`.
4. `TenantQuerierQueuingAlgorithm` iterates `tenantIDOrder` starting after `LastTenantIndex`, returning the first tenant that: (a) has a queue under this query component node, and (b) is sharded to this querier (or has no shard restriction).
5. If no tenant is found, the search backtracks to try the next query component subtree.

## Per-Tenant Queueing

**Per-tenant queueing already exists.** Each tenant gets its own leaf-node FIFO queue within each query component partition. The following isolation mechanisms are in place:

### Round-robin fairness

`TenantQuerierQueuingAlgorithm` maintains a shared `tenantIDOrder` list and `tenantOrderIndex`. Each dequeue advances the index, cycling through all tenants. This is shared across all query component nodes so that a tenant's time-to-dequeue is O(n) where n = number of tenants, regardless of how many query component partitions exist.

### Shuffle-sharding

When `MaxQueriersPerUser` > 0 (set via `-query-frontend.max-queriers-per-tenant`), a tenant is assigned a deterministic subset of queriers based on a shuffle-shard seed derived from the tenant ID. Only assigned queriers can dequeue that tenant's requests. This is managed by `tenantQuerierShards` and pushed into `TenantQuerierQueuingAlgorithm` via `SetQueriersForTenant`.

### Per-tenant queue depth limit

`maxOutstandingPerTenant` (flag: `-query-scheduler.max-outstanding-requests-per-tenant`, default 100) caps the total number of queued requests for a tenant across all query component partitions. When exceeded, new requests are rejected with HTTP 429 (`ErrTooManyRequests`). The count is computed by `TotalQueueSizeForTenant`, which sums `ItemCount()` across all of a tenant's tree nodes.

### Per-tenant metrics

- `cortex_query_scheduler_queue_length` (gauge, per user) — current queue depth
- `cortex_query_scheduler_discarded_requests_total` (counter, per user) — rejected requests (queue full)
- `cortex_query_scheduler_cancelled_requests_total` (counter, per user) — cancelled requests

## Query Component Isolation

The `QuerierWorkerQueuePriorityAlgo` partitions querier-worker connections across up to 4 query component subtrees using `WorkerID % len(nodeOrder)`. This ensures that when one query component (e.g., store-gateway) is experiencing high latency, approximately 25% of querier-workers remain dedicated to servicing queries for unaffected components (e.g., ingester-only queries).

Workers are *prioritized* to start at their assigned component but will fall through to other components if their subtree has no dequeueable requests. This prevents idle capacity when a component's queue is empty.

The algorithm requires a minimum of 4 querier-worker connections per querier (enforced by overriding `-querier.max-concurrent` if set lower).

See `DESIGN.md` in this directory for detailed analysis, benchmarks, and diagrams comparing this approach against the previous round-robin strategy.

## Possible Enhancements to Per-Tenant Queueing

While per-tenant queueing exists, these areas could be further developed:

| Feature | Current State | Enhancement Possibility |
|---------|--------------|------------------------|
| Queue depth limit | Global setting (`max-outstanding-requests-per-tenant`) applies uniformly | Per-tenant configurable limits via the `Limits` interface |
| Priority | All tenants equal (round-robin) | Weighted fair queuing or priority tiers |
| Rate limiting | Only queue depth is checked at enqueue time | Per-tenant query rate limits (token bucket, etc.) |
| Scheduling | Round-robin | SLO-aware or deadline-based scheduling |

**Extension points**: The `Limits` interface (`scheduler.go:228`) currently exposes only `MaxQueriersPerUser`. Adding methods here (e.g., `MaxOutstandingPerUser`) and threading them through `RequestQueue` -> `queueBroker` -> `enqueueRequestBack` would be the natural path for per-tenant queue depth configuration. The `QueuingAlgorithm` interface in the tree package allows plugging in alternative tenant selection strategies without modifying the tree structure.

## Key Files

| File | Role |
|------|------|
| `scheduler.go` | Main scheduler service: `FrontendLoop` (enqueue), `QuerierLoop` (dequeue), `enqueueRequest` |
| `queue/queue.go` | `RequestQueue`: single-threaded `dispatcherLoop`, channel-based coordination |
| `queue/queue_broker.go` | `queueBroker`: brokers tree access, builds `QueuePath`, enforces queue depth, manages querier-tenant relationships |
| `queue/tenant_querier_shards.go` | `tenantQuerierShards`: shuffle-sharding logic, tenant-querier assignment computation |
| `queue/querier_connections.go` | `querierConnections`: querier lifecycle (connect, disconnect, shutdown, forget delay) |
| `queue/tree/multi_algorithm_tree_queue.go` | `MultiAlgorithmTreeQueue`: tree data structure, recursive dequeue with per-depth algorithms |
| `queue/tree/tenant_querier_queuing_algorithm.go` | `TenantQuerierQueuingAlgorithm`: tenant round-robin + shuffle-shard enforcement at level 2 |
| `queue/tree/tree_queue_algo_querier_worker_queue_priority.go` | `QuerierWorkerQueuePriorityAlgo`: worker-to-component partitioning at level 1 |
| `DESIGN.md` | Detailed design doc with diagrams, benchmarks, and rationale for queue prioritization |
