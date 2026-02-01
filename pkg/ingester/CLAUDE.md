# Kafka Ingester Implementation - Technical Documentation

This document describes the Kafka-based ingest storage implementation in Grafana Mimir, focusing on Prometheus metrics instrumentation.

## Overview

The Kafka ingester is an alternative ingestion path for Mimir that uses Kafka as an intermediate storage layer before data reaches the TSDB. When enabled, write requests are first written to Kafka partitions and then consumed by ingesters to be stored in their local TSDBs.

### Architecture Flow

```
Write Request → Distributor → Kafka Topic → Partition Reader → TSDB Storage
```

### Key Components

1. **Writer** (`pkg/storage/ingest/writer.go`) - Produces records to Kafka
2. **Partition Reader** (`pkg/storage/ingest/reader.go`) - Consumes records from Kafka partitions
3. **Pusher** (`pkg/storage/ingest/pusher.go`) - Pushes consumed records to TSDB storage
4. **Partition Offset Client** (`pkg/storage/ingest/partition_offset_client.go`) - Manages Kafka offsets

## Configuration

The Kafka ingester is enabled via the `ingest-storage.enabled` flag. Key configuration is in `KafkaConfig` struct (`pkg/storage/ingest/config.go`):

- `address` - Kafka broker address
- `topic` - Kafka topic name
- `write-clients` - Number of Kafka producer clients
- `fetch-concurrency-max` - Max concurrent fetch requests
- `ingestion-concurrency-max` - Max concurrent ingestion streams to TSDB

## Prometheus Metrics Implementation

### 1. Ingester Core Metrics (`pkg/ingester/metrics.go`)

The standard ingester metrics (non-Kafka specific) are defined in `ingesterMetrics` struct:

| Metric | Type | Description |
|--------|------|-------------|
| `cortex_ingester_ingested_samples_total` | Counter | Total samples ingested per user |
| `cortex_ingester_ingested_samples_failures_total` | Counter | Failed sample ingestions per user |
| `cortex_ingester_ingested_exemplars_total` | Counter | Total exemplars ingested |
| `cortex_ingester_ingested_metadata_total` | Counter | Total metadata ingested |
| `cortex_ingester_queries_total` | Counter | Total queries handled |
| `cortex_ingester_queried_samples` | Histogram | Samples returned from queries |
| `cortex_ingester_queried_series` | Histogram | Series returned from queries |
| `cortex_ingester_memory_series` | Gauge | Current series in memory |
| `cortex_ingester_memory_metadata` | Gauge | Current metadata in memory |
| `cortex_ingester_memory_users` | Gauge | Current users in memory |
| `cortex_ingester_active_series` | Gauge | Active series per user |
| `cortex_ingester_owned_series` | Gauge | Owned series per user |
| `cortex_ingester_tsdb_compactions_triggered_total` | Counter | TSDB compactions triggered |
| `cortex_ingester_instance_limits` | Gauge | Instance limits (max_tenants, max_series, etc.) |

### 2. Kafka Writer Metrics (`pkg/storage/ingest/writer.go`)

Prefix: `cortex_ingest_storage_writer_`

| Metric | Type | Description |
|--------|------|-------------|
| `cortex_ingest_storage_writer_latency_seconds` | Histogram | Latency to write to Kafka (labels: `outcome=success/failure`) |
| `cortex_ingest_storage_writer_sent_bytes_total` | Counter | Total bytes sent to Kafka |
| `cortex_ingest_storage_writer_records_per_write_request` | Histogram | Records per write request |

### 3. Kafka Producer Client Metrics (`pkg/storage/ingest/writer_client.go`)

Per-client metrics (wrapped with `client_id` label):

| Metric | Type | Description |
|--------|------|-------------|
| `buffered_produce_bytes` | Summary | Buffered produce records in bytes |
| `buffered_produce_bytes_limit` | Gauge | Bytes limit on buffered records |
| `produce_records_enqueued_total` | Counter | Records enqueued to send |
| `produce_records_failed_total` | Counter | Failed records (labels: `reason`) |
| `produce_records_enqueue_duration_seconds` | Histogram | Time to enqueue records |
| `produce_remaining_deadline_seconds` | Histogram | Remaining deadline when producing |

### 4. Kafka Reader Metrics (`pkg/storage/ingest/reader.go`)

Prefix: `cortex_ingest_storage_reader`

| Metric | Type | Description |
|--------|------|-------------|
| `cortex_ingest_storage_reader_receive_delay_seconds` | Histogram | Delay between producing and receiving (labels: `phase=starting/running`) |
| `cortex_ingest_storage_reader_last_consumed_offset` | Gauge | Last consumed offset per partition |
| `cortex_ingest_storage_reader_buffered_fetched_records` | Gauge | Buffered records not yet processed |
| `cortex_ingest_storage_reader_buffered_fetched_bytes` | Gauge | Buffered bytes not yet processed |
| `cortex_ingest_storage_reader_estimated_bytes_per_record` | Histogram | Estimated record size |
| `cortex_ingest_storage_reader_records_per_fetch` | Histogram | Records per fetch operation |
| `cortex_ingest_storage_reader_fetch_errors_total` | Counter | Fetch errors encountered |
| `cortex_ingest_storage_reader_fetches_total` | Counter | Total Kafka fetches |
| `cortex_ingest_storage_reader_records_batch_wait_duration_seconds` | Histogram | Time waiting for batch |
| `cortex_ingest_storage_reader_records_batch_fetch_max_bytes` | Histogram | MaxBytes in Fetch requests |
| `cortex_ingest_storage_reader_fetched_discarded_bytes_total` | Counter | Discarded bytes (already consumed) |
| `cortex_ingest_storage_reader_records_batch_process_duration_seconds` | Histogram | Batch processing duration |
| `cortex_ingest_storage_reader_missed_records_total` | Counter | Offsets never consumed |

### 5. Partition Committer Metrics (`pkg/storage/ingest/reader.go`)

| Metric | Type | Description |
|--------|------|-------------|
| `cortex_ingest_storage_reader_offset_commit_requests_total` | Counter | Offset commit requests per partition |
| `cortex_ingest_storage_reader_offset_commit_failures_total` | Counter | Failed offset commits per partition |
| `cortex_ingest_storage_reader_offset_commit_request_duration_seconds` | Histogram | Commit request duration |
| `cortex_ingest_storage_reader_last_committed_offset` | Gauge | Last committed offset per partition |

### 6. Partition Offset Client Metrics (`pkg/storage/ingest/partition_offset_client.go`)

| Metric | Type | Description |
|--------|------|-------------|
| `cortex_ingest_storage_reader_last_produced_offset_requests_total` | Counter | Requests for last produced offset |
| `cortex_ingest_storage_reader_last_produced_offset_failures_total` | Counter | Failed offset requests |
| `cortex_ingest_storage_reader_last_produced_offset_request_duration_seconds` | Histogram | Request duration |
| `cortex_ingest_storage_reader_partition_start_offset_requests_total` | Counter | Requests for partition start offset |
| `cortex_ingest_storage_reader_partition_start_offset_failures_total` | Counter | Failed start offset requests |
| `cortex_ingest_storage_reader_partition_start_offset_request_duration_seconds` | Histogram | Start offset request duration |

### 7. Pusher Consumer Metrics (`pkg/storage/ingest/pusher_metrics.go`)

| Metric | Type | Description |
|--------|------|-------------|
| `cortex_ingest_storage_reader_records_processing_time_seconds` | Histogram | Time to process fetched records batch |
| `cortex_ingest_storage_reader_requests_failed_total` | Counter | Write request failures (labels: `cause=client/server`) |
| `cortex_ingest_storage_reader_requests_total` | Counter | Total write requests after batching |
| `cortex_ingest_storage_reader_pusher_batch_age_seconds` | Histogram | Age of sample batch being ingested |
| `cortex_ingest_storage_reader_pusher_processing_time_seconds` | Histogram | Time to ingest batch (labels: `content`) |
| `cortex_ingest_storage_reader_pusher_timeseries_per_flush` | Histogram | Timeseries per flush to shard |
| `cortex_ingest_storage_reader_shards_per_push` | Histogram | Shards pushed per batch |
| `cortex_ingest_storage_reader_pushers_per_push` | Histogram | Pushers per batch |
| `cortex_ingest_storage_reader_pusher_estimated_timeseries_total` | Counter | Estimated timeseries per shard |
| `cortex_ingest_storage_reader_batching_queue_flush_total` | Counter | Batch flush operations |
| `cortex_ingest_storage_reader_batching_queue_flush_errors_total` | Counter | Batch flush errors |

### 8. Strong Consistency Metrics (`pkg/storage/ingest/reader.go`)

| Metric | Type | Description |
|--------|------|-------------|
| `cortex_ingest_storage_strong_consistency_requests_total` | Counter | Strong consistency requests (labels: `with_offset`, `topic`, `component`) |
| `cortex_ingest_storage_strong_consistency_failures_total` | Counter | Failed strong consistency waits |
| `cortex_ingest_storage_strong_consistency_wait_duration_seconds` | Histogram | Time waiting for strong consistency |

### 9. Kafka Client Extended Metrics (`pkg/storage/ingest/kafka_client_metrics.go`)

Custom metrics using native histograms for deeper Kafka observability:

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `kafka_write_wait_seconds` | Histogram | - | Time waiting to write to Kafka |
| `kafka_write_time_seconds` | Histogram | - | Time spent writing to Kafka |
| `kafka_read_wait_seconds` | Histogram | - | Time waiting to read from Kafka |
| `kafka_read_time_seconds` | Histogram | - | Time spent reading from Kafka |
| `kafka_request_duration_e2e_seconds` | Histogram | - | End-to-end Kafka request duration |
| `kafka_request_throttled_seconds` | Histogram | - | Time requests were throttled |
| `kafka_broker_read_bytes_total` | Counter | `node_id` | Total bytes read from each Kafka broker |
| `kafka_broker_write_bytes_total` | Counter | `node_id` | Total bytes written to each Kafka broker |

The per-broker network traffic metrics (`kafka_broker_read_bytes_total` and `kafka_broker_write_bytes_total`) use `kgo.HookBrokerRead` and `kgo.HookBrokerWrite` hooks to track network I/O per broker node. The `node_id` label contains the Kafka broker's node ID (may be negative for seed brokers). Note that these metrics do not include TLS overhead.

### 10. kprom (franz-go) Standard Metrics

The franz-go Kafka client library (`kprom`) provides standard metrics automatically registered with the `component` label:

- Connection metrics
- Request/response metrics
- Batch metrics
- Compression metrics
- Record byte metrics

Registered via `NewKafkaReaderClientMetrics()` with prefix `cortex_ingest_storage_reader`.

## Native Histogram Support

Many Kafka-related metrics use Prometheus native histograms for more efficient storage and querying:

```go
prometheus.HistogramOpts{
    NativeHistogramBucketFactor:     1.1,
    NativeHistogramMaxBucketNumber:  100,
    NativeHistogramMinResetDuration: 1 * time.Hour,
}
```

## TSDB Metrics Aggregation

The `tsdbMetrics` struct (`pkg/ingester/metrics.go`) aggregates per-tenant TSDB metrics into global metrics using `dskit_metrics.TenantRegistries`. This allows collecting metrics across all tenant TSDBs while keeping cardinality manageable.

Key aggregated metrics include:
- `cortex_ingester_tsdb_compactions_total`
- `cortex_ingester_tsdb_head_series`
- `cortex_ingester_tsdb_head_chunks`
- `cortex_ingester_tsdb_wal_truncations_total`
- `cortex_ingester_memory_series`

## Implementation Details

### Partition Ownership

Each ingester owns a single Kafka partition. The partition ID is derived from the ingester instance ID:

```go
// pkg/storage/ingest/util.go
func IngesterPartitionID(ingesterID string) (int32, error)
```

### Consumer Group

Each ingester uses a unique consumer group to track its last consumed offset, ensuring exactly-once processing semantics during restarts.

### Lifecycle Integration

The Kafka reader integrates with dskit's service lifecycle:
1. During startup: Replays partition until lag is below threshold
2. During running: Continuously processes new records
3. During shutdown: Commits final offset and gracefully stops

### Error Handling

Metrics differentiate between client errors (e.g., tenant limits) and server errors (e.g., internal failures):
- `cortex_ingest_storage_reader_requests_failed_total{cause="client"}`
- `cortex_ingest_storage_reader_requests_failed_total{cause="server"}`

## Data Flow (Ingest Storage Mode)

```
Kafka Topic
    |
    v
PartitionReader (reader.go)
    |-- concurrentFetchers (fetcher.go) [if fetch-concurrency-max > 0]
    |
    v
pusherConsumer.Consume() (pusher.go)
    |
    +-- Unmarshal records (parallel goroutine)
    |
    v
parallelStoragePusher / sequentialStoragePusher
    |
    +-- parallelStorageShards [if ingestion-concurrency-max > 0]
    |       |-- batchingQueue per shard
    |       |-- Hash timeseries to shards
    |
    v
Ingester.PushToStorageAndReleaseRequest()
    |
    v
TSDB (per tenant)
```

## Concurrent Fetchers (`pkg/storage/ingest/fetcher.go`)

When `fetch-concurrency-max > 0`, the reader uses `concurrentFetchers` which:

1. Spawns multiple goroutines to fetch records in parallel
2. Estimates bytes per record to optimize fetch requests
3. Maintains order by buffering and reordering results
4. Uses `fetchWant` struct to track desired offset ranges

Key constants:
- `initialBytesPerRecord = 10_000` - Initial estimation
- `forcedMinValueForMaxBytes = 1_000_000` - Minimum fetch size

### Fetcher Interface

```go
type fetcher interface {
    PollFetches(context.Context) (kgo.Fetches, context.Context)
    Start(ctx context.Context)
    Stop()
    BufferedRecords() int64
    BufferedBytes() int64
    EstimatedBytesPerRecord() int64
}
```

## Parallel Ingestion

The pusher supports two modes controlled by `ingestion-concurrency-max`:

### Sequential Mode (default, when max=0)
`sequentialStoragePusher` - processes records one by one, simpler overhead.

### Parallel Mode (when max>0)
`parallelStoragePusher` → `parallelStorageShards` → `batchingQueue`

- Creates shards per tenant+source combination
- Hash timeseries to shards using xxhash
- Each shard has a `batchingQueue` that batches samples before flushing
- Batch size controlled by `ingestion-concurrency-batch-size`

Key metrics for parallel mode:
- `cortex_ingest_storage_reader_shards_per_push` - Shards used
- `cortex_ingest_storage_reader_pusher_batch_age_seconds` - Batch age

## Error Handling in Kafka Fetch (`fetcher.go`)

The `handleKafkaFetchErr()` function handles errors:

| Error | Action |
|-------|--------|
| `kerr.OffsetOutOfRange` | Adjust start offset or wait for new records |
| `kerr.TopicAuthorizationFailed` | Backoff |
| `kerr.UnknownTopicOrPartition` | Backoff |
| `kerr.NotLeaderForPartition` | Refresh metadata + backoff |
| `kerr.ReplicaNotAvailable` | Refresh metadata + backoff |
| `kerr.UnknownLeaderEpoch` | Refresh metadata + backoff |
| `kerr.FencedLeaderEpoch` | Refresh metadata + backoff |
| `kerr.LeaderNotAvailable` | Refresh metadata + backoff |
| Unknown broker / connection errors | Immediate retry |
| `i/o timeout` | Refresh metadata + backoff |

## Writer Implementation Details

### Record Serialization (`writer.go`)

The `recordSerializer` interface supports different serialization formats:
- Records may be split if larger than `maxProducerRecordDataBytesLimit` (16MB - 16KB overhead)
- Minimum record size: `minProducerRecordDataBytesLimit` (1MB)

### Producer Configuration

- Uses `AllISRAcks()` for durability
- Manual partitioner (partition ID set per record)
- Producer linger: 50ms (for batching efficiency)
- Max inflight requests per broker: 20
- No idempotent writes (disabled for higher throughput)

## Related Files

- `pkg/ingester/ingester.go` - Main ingester implementation with Kafka integration
- `pkg/ingester/metrics.go` - Ingester and TSDB metrics
- `pkg/ingester/ingester_ingest_storage_test.go` - Integration tests
- `pkg/storage/ingest/config.go` - Kafka configuration
- `pkg/storage/ingest/reader.go` - Kafka partition reader
- `pkg/storage/ingest/fetcher.go` - Concurrent fetchers implementation
- `pkg/storage/ingest/writer.go` - Kafka writer
- `pkg/storage/ingest/writer_client.go` - Kafka producer client
- `pkg/storage/ingest/reader_client.go` - Kafka consumer client
- `pkg/storage/ingest/pusher.go` - Record consumer and storage pusher
- `pkg/storage/ingest/pusher_metrics.go` - Pusher metrics
- `pkg/storage/ingest/kafka_client_metrics.go` - Custom Kafka client metrics
- `pkg/storage/ingest/partition_offset_client.go` - Offset management
