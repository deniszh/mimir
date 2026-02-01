# Distributor Kafka/Ingest Storage Implementation - Technical Documentation

This document describes the Kafka-based ingest storage implementation in the Grafana Mimir distributor, focusing on Prometheus metrics instrumentation.

## Overview

The distributor is responsible for receiving write requests and routing them to either:
1. **Classic storage path**: Direct push to ingesters via gRPC
2. **Ingest storage path**: Writing to Kafka partitions (when enabled)
3. **Migration mode**: Both paths simultaneously (for migration purposes)

### Architecture Flow

```
Write Request → Distributor → Validation → Rate Limiting → HA Tracking →
    ├─→ Kafka Partitions (ingest storage)
    └─→ Ingesters (classic storage)
```

### Key Components

1. **Distributor** (`pkg/distributor/distributor.go`) - Main request handler
2. **Ingest Storage Writer** (`pkg/storage/ingest/writer.go`) - Kafka producer
3. **Validation** (`pkg/distributor/validate.go`) - Sample/exemplar/metadata validation
4. **HA Tracker** (`pkg/distributor/ha_tracker.go`) - High availability replica tracking
5. **Push Handler** (`pkg/distributor/push.go`) - HTTP/gRPC request handling

## Configuration

The Kafka ingest storage is enabled via `ingest-storage.enabled` flag. Key distributor configuration:

```go
type Config struct {
    IngestStorageConfig ingest.Config `yaml:"-"`
    // ...
}
```

Migration-specific options:
- `ingest-storage.migration.distributor-send-to-ingesters-enabled` - Write to both backends
- `ingest-storage.migration.ignore-ingest-storage-errors` - Log but don't fail on Kafka errors
- `ingest-storage.migration.ingest-storage-max-wait-time` - Timeout for Kafka writes during migration

## Prometheus Metrics Implementation

### 1. Core Distributor Metrics (`pkg/distributor/distributor.go`)

#### Request/Sample Counters

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `cortex_distributor_requests_in_total` | Counter | `user` | Total requests received |
| `cortex_distributor_received_requests_total` | Counter | `user` | Requests after deduplication |
| `cortex_distributor_samples_in_total` | Counter | `user` | Total samples received |
| `cortex_distributor_received_samples_total` | Counter | `user` | Samples after deduplication |
| `cortex_distributor_exemplars_in_total` | Counter | `user` | Total exemplars received |
| `cortex_distributor_received_exemplars_total` | Counter | `user` | Exemplars after deduplication |
| `cortex_distributor_metadata_in_total` | Counter | `user` | Total metadata received |
| `cortex_distributor_received_metadata_total` | Counter | `user` | Metadata after deduplication |

#### Native Histogram Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `cortex_distributor_received_native_histograms_total` | Counter | `user` | Native histogram samples received |
| `cortex_distributor_received_native_histogram_buckets_total` | Counter | `user` | Native histogram buckets received |
| `cortex_distributor_dropped_native_histograms_total` | Counter | `user` | Native histograms dropped (ingestion disabled) |

#### HA Deduplication Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `cortex_distributor_non_ha_samples_received_total` | Counter | `user` | Samples from non-HA sources |
| `cortex_distributor_deduped_samples_total` | Counter | `user`, `cluster` | Samples deduplicated via HA |

#### Query/Latency Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `cortex_distributor_query_duration_seconds` | Histogram | `method`, `status_code` | Query duration |
| `cortex_distributor_sample_delay_seconds` | Histogram | `user` | Delay between sample timestamp and receipt |
| `cortex_distributor_samples_per_request` | Histogram | `user` | Samples per request (native histogram) |
| `cortex_distributor_exemplars_per_request` | Histogram | `user` | Exemplars per request (native histogram) |
| `cortex_distributor_latest_seen_sample_timestamp_seconds` | Gauge | `user` | Unix timestamp of latest sample |

#### Labels Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `cortex_distributor_labels_count` | Histogram | Number of labels per series |
| `cortex_distributor_hash_collisions_total` | Counter | Hash collisions during deduplication |

### 2. Instance Limits Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `cortex_distributor_instance_limits` | Gauge | `limit` | Instance limits configuration |
| `cortex_distributor_inflight_push_requests` | Gauge | - | Current inflight push requests |
| `cortex_distributor_inflight_push_requests_bytes` | Gauge | - | Current inflight request bytes |
| `cortex_distributor_ingestion_rate_samples_per_second` | Gauge | - | Current ingestion rate |
| `cortex_distributor_instance_rejected_requests_total` | Counter | `reason` | Rejected requests per reason |

Limit labels:
- `max_inflight_push_requests`
- `max_inflight_push_requests_bytes`
- `max_ingestion_rate`

Rejection reasons:
- `distributor_max_ingestion_rate`
- `distributor_max_inflight_push_requests`
- `distributor_max_inflight_push_requests_bytes`

### 3. Ingest Storage Mode Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `cortex_distributor_ingest_storage_enabled` | Gauge | 1 if ingest storage enabled, 0 if disabled |
| `cortex_distributor_replication_factor` | Gauge | Configured replication factor (classic storage only) |

### 4. Push Handler Metrics (`pkg/distributor/distributor.go`)

#### Influx Push Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `cortex_distributor_influx_requests_total` | Counter | `user` | Total Influx write requests |
| `cortex_distributor_influx_uncompressed_body_bytes` | Histogram | `user` | Influx request body size |

#### OTLP Push Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `cortex_distributor_otlp_requests_total` | Counter | `user` | Total OTLP write requests |
| `cortex_distributor_uncompressed_body_bytes` | Histogram | `user` | OTLP request body size |

### 5. Validation Metrics (`pkg/distributor/validate.go`)

All validation metrics use the `cortex_discarded_samples_total`, `cortex_discarded_exemplars_total`, `cortex_discarded_metadata_total`, or `cortex_discarded_requests_total` metric names with a `reason` const label.

#### Sample Validation Reasons

| Reason Label | Description |
|--------------|-------------|
| `missing_metric_name` | Series has no `__name__` label |
| `invalid_metric_name` | Invalid metric name format |
| `max_label_names_per_series` | Too many labels |
| `max_label_names_per_info_series` | Too many labels on `_info` series |
| `invalid_label` | Invalid label name |
| `invalid_label_value` | Invalid label value |
| `label_name_too_long` | Label name exceeds limit |
| `label_value_too_long` | Label value exceeds limit |
| `max_native_histogram_buckets` | Native histogram has too many buckets |
| `invalid_schema_native_histogram` | Invalid native histogram schema |
| `duplicate_label_names` | Duplicate label names in series |
| `sample_too_far_in_future` | Sample timestamp too far in future |
| `sample_too_far_in_past` | Sample timestamp too far in past |
| `duplicate_timestamp` | Duplicate timestamp for same series |

#### Exemplar Validation Reasons

| Reason Label | Description |
|--------------|-------------|
| `exemplar_labels_missing` | Exemplar has no labels |
| `exemplar_timestamp_invalid` | Exemplar has no timestamp |
| `exemplar_labels_too_long` | Exemplar labels exceed 128 chars |
| `exemplar_labels_blank` | Exemplar labels are blank |
| `exemplar_too_old` | Exemplar timestamp too old |
| `exemplar_too_far_in_future` | Exemplar timestamp too far in future |
| `too_many_exemplars_per_series_per_request` | Too many exemplars per series |

#### Rate Limiting Reasons

| Reason Label | Metric | Description |
|--------------|--------|-------------|
| `rate_limited` | `cortex_discarded_samples_total` | Samples rate limited |
| `rate_limited` | `cortex_discarded_requests_total` | Requests rate limited |
| `rate_limited` | `cortex_discarded_exemplars_total` | Exemplars rate limited |
| `rate_limited` | `cortex_discarded_metadata_total` | Metadata rate limited |
| `too_many_ha_clusters` | `cortex_discarded_samples_total` | Too many HA clusters |

### 6. HA Tracker Metrics (`pkg/distributor/ha_tracker.go`)

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `cortex_ha_tracker_elected_replica_status` | Counter | `user`, `cluster`, `replica` | Elected replica status (optional) |
| `cortex_ha_tracker_elected_replica_changes_total` | Counter | `user`, `cluster` | Replica election changes |
| `cortex_ha_tracker_elected_replica_timestamp_seconds` | Gauge | `user`, `cluster` | Elected replica timestamp |
| `cortex_ha_tracker_last_election_timestamp_seconds` | Gauge | `user`, `cluster` | Last election timestamp |
| `cortex_ha_tracker_total_reelections` | Gauge | `user`, `cluster` | Total reelections |
| `cortex_ha_tracker_elected_replica_propagation_time_seconds` | Histogram | - | Replica propagation time |
| `cortex_ha_tracker_kv_store_cas_total` | Counter | `user`, `status` | KV store CAS operations |
| `cortex_ha_tracker_replica_cleanup_runs_started_total` | Counter | - | Cleanup runs started |
| `cortex_ha_tracker_replicas_marked_for_deletion_total` | Counter | - | Replicas marked for deletion |
| `cortex_ha_tracker_deleted_replicas_total` | Counter | - | Replicas deleted |
| `cortex_ha_tracker_marking_for_deletion_failed_total` | Counter | - | Failed deletion markings |
| `cortex_ha_tracker_replica_desc_failed_type_assertions_total` | Counter | - | Failed type assertions |

### 7. Ingester Client Metrics (`pkg/ingester/client/metrics.go`)

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `cortex_ingester_client_request_duration_seconds` | Histogram | `operation`, `status_code` | Ingester client request duration |

### 8. Kafka Writer Metrics (from `pkg/storage/ingest/writer.go`)

When ingest storage is enabled, the distributor uses the Writer which exposes:

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `cortex_ingest_storage_writer_latency_seconds` | Histogram | `outcome` | Latency to write to Kafka |
| `cortex_ingest_storage_writer_sent_bytes_total` | Counter | - | Total bytes sent to Kafka |
| `cortex_ingest_storage_writer_records_per_write_request` | Histogram | - | Records per write request |
| `cortex_ingest_storage_writer_kafka_broker_read_bytes_total` | Counter | `node_id` | Total bytes read from each Kafka broker |
| `cortex_ingest_storage_writer_kafka_broker_write_bytes_total` | Counter | `node_id` | Total bytes written to each Kafka broker |

Plus per-client metrics (see `pkg/ingester/CLAUDE.md` for full list).

## Implementation Details

### Partition Sharding

Series are sharded across Kafka partitions using consistent hashing:

```go
func getTokensForSeries(userID string, series []mimirpb.PreallocTimeseries) []uint32 {
    // Returns hash tokens for ring-based sharding
}
```

When shuffle sharding is enabled (`ingestion_partitions_tenant_shard_size`), each tenant uses a subset of partitions.

### Write Flow with Ingest Storage

1. Request received and validated
2. Rate limiting applied
3. HA deduplication (if enabled)
4. Series tokens calculated
5. `sendWriteRequestToBackends()` routes to:
   - `sendWriteRequestToPartitions()` - writes to Kafka
   - `sendWriteRequestToIngesters()` - writes to ingesters (migration mode)

### Migration Mode

During migration, the distributor can write to both backends:
```go
if d.cfg.IngestStorageConfig.Migration.DistributorSendToIngestersEnabled {
    // Write to both Kafka and ingesters
}
```

Error handling in migration mode:
- If `IgnoreIngestStorageErrors` is true, Kafka errors are logged but don't fail the request
- Partition errors take precedence over ingester errors (5xx vs 4xx)

### Partition ID Handling

Partition IDs are stored as strings in the ring:
```go
partitionID, err := strconv.ParseUint(partition.Id, 10, 31)
err = d.ingestStorageWriter.WriteSync(ctx, int32(partitionID), tenantID, req)
```

### Error Wrapping

Errors from Kafka are wrapped to identify the source:
```go
err = wrapPartitionPushError(err, int32(partitionID))
err = wrapDeadlineExceededPushError(err)
return errors.Wrap(err, "send data to partitions")
```

## Native Histogram Support

Native histograms have dedicated metrics tracking:
- Uses `NativeHistogramBucketFactor` for efficient histogram buckets
- Separate counters for samples vs. bucket counts
- Validation for schema and bucket limits

## Related Files

- `pkg/distributor/distributor.go` - Main distributor implementation
- `pkg/distributor/distributor_ingest_storage_test.go` - Kafka integration tests
- `pkg/distributor/validate.go` - Validation logic and metrics
- `pkg/distributor/ha_tracker.go` - HA replica tracking
- `pkg/distributor/push.go` - Push request handling
- `pkg/distributor/otel.go` - OTLP metrics conversion
- `pkg/storage/ingest/writer.go` - Kafka producer
- `pkg/util/validation/discarded_metrics.go` - Discarded metrics helpers

## See Also

- `pkg/ingester/CLAUDE.md` - Ingester-side Kafka implementation
- `pkg/storage/ingest/DESIGN.md` - Ingest storage design documentation
