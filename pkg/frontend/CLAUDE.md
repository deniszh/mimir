# Query Frontend - Technical Documentation

This document describes the Grafana Mimir query frontend implementation, with a focus on HTTP 429 (Too Many Requests) and 499 (Client Closed Request) error codes.

## Overview

The query frontend is the entry point for all query traffic in Grafana Mimir. It receives HTTP queries from clients, applies middleware (caching, splitting, sharding), queues requests, and dispatches them to queriers for execution.

## Package Structure

```
pkg/frontend/
├── config.go                 # Combined frontend configuration and initialization
├── downstream_roundtripper.go # RoundTripper for forwarding to downstream URL
├── transport/
│   └── handler.go            # HTTP handler for query requests
├── v1/
│   └── frontend.go           # Frontend V1 (without scheduler)
├── v2/
│   ├── frontend.go           # Frontend V2 (with scheduler)
│   └── frontend_scheduler_worker.go # Workers for scheduler communication
└── querymiddleware/
    ├── codec.go              # Request/response encoding/decoding
    ├── errors.go             # Error type definitions
    ├── limits.go             # Query limit enforcement
    ├── query_limiter.go      # Query rate limiting middleware
    └── retry.go              # Retry logic for failed queries
```

## Frontend Modes

The frontend supports two operational modes:

1. **V1 Frontend** (`pkg/frontend/v1/`): Standalone frontend that queues requests internally and dispatches them to queriers directly.

2. **V2 Frontend** (`pkg/frontend/v2/`): Frontend that works with a query-scheduler. Requests are forwarded to the scheduler which manages queuing and dispatch to queriers.

Mode selection is automatic based on configuration in `config.go`:
- V2 is used when `-query-scheduler.address` is configured or ring-based scheduler discovery is enabled
- V1 is used otherwise

## Request Flow Diagram

```
                                    ┌─────────────────────────────────────────┐
                                    │           Query Frontend                │
                                    │                                         │
   HTTP Request                     │  ┌─────────────────────────────────┐   │
────────────────────────────────────┼─►│    transport.Handler            │   │
                                    │  │    (ServeHTTP)                  │   │
                                    │  └──────────────┬──────────────────┘   │
                                    │                 │                       │
                                    │                 ▼                       │
                                    │  ┌─────────────────────────────────┐   │
                                    │  │   Query Middleware Chain        │   │
                                    │  │   - limitsMiddleware            │   │
                                    │  │   - queryLimiterMiddleware ─────┼───┼──► 429 (rate limit)
                                    │  │   - retryMiddleware             │   │
                                    │  │   - ...                         │   │
                                    │  └──────────────┬──────────────────┘   │
                                    │                 │                       │
                           ┌────────┼─────────────────┼───────────────────────┘
                           │        │                 │
            ┌──────────────┴──┐     │     ┌───────────┴──────────────┐
            │   Frontend V1   │     │     │      Frontend V2         │
            │                 │     │     │                          │
            │  requestQueue   │     │     │  frontendSchedulerWorker │
            │       │         │     │     │           │              │
            │       ▼         │     │     │           ▼              │
            │  Queue Full?────┼─────┼─────┼─► 429    Query-Scheduler │
            │                 │     │     │           │              │
            └────────┬────────┘     │     │           ▼              │
                     │              │     │    Queue Full? ──────────┼──► 429
                     │              │     └───────────┬──────────────┘
                     │              │                 │
                     └──────────────┼─────────────────┘
                                    │
                                    ▼
                               ┌─────────┐
                               │ Querier │
                               └─────────┘

   Client Cancellation at any point ──────────────────────────────────► 499
```

---

## HTTP 429 (Too Many Requests)

HTTP 429 is returned when the system cannot accept additional queries due to queue capacity limits or rate limiting. There are four distinct sources:

### Summary Table

| Source | File | Configuration | Error Message |
|--------|------|---------------|---------------|
| V1 Queue Overflow | `v1/frontend.go:36,364-365` | `-querier.max-outstanding-requests-per-tenant` | `too many outstanding requests` |
| V2 Scheduler Response | `v2/frontend_scheduler_worker.go:457-466` | `-query-scheduler.max-outstanding-requests-per-tenant` | `too many outstanding requests` |
| Query Limiter | `querymiddleware/query_limiter.go:82-88` | Per-tenant `limited_queries` | `the query has been limited...` |
| Codec Decoding | `querymiddleware/codec.go` | N/A | (downstream error passthrough) |

---

### 1. V1 Frontend Queue Overflow

**Location:** `pkg/frontend/v1/frontend.go`

When using V1 frontend (without query-scheduler), each tenant has a bounded request queue. When this queue is full, new requests are rejected with HTTP 429.

**Error Definition (line 36):**
```go
var errTooManyRequest = httpgrpc.Errorf(http.StatusTooManyRequests, "too many outstanding requests")
```

**Trigger Point (lines 364-365):**
```go
func (f *Frontend) queueRequest(ctx context.Context, req *request) error {
    // ...
    err = f.requestQueue.SubmitRequestToEnqueue(joinedTenantID, req, maxQueriers, nil)
    if errors.Is(err, queue.ErrTooManyRequests) {
        return errTooManyRequest
    }
    return err
}
```

**Configuration:**
```
-querier.max-outstanding-requests-per-tenant (default: 100)
```

Maximum number of outstanding requests per tenant per frontend. When exceeded, additional requests receive HTTP 429.

---

### 2. V2 Frontend (Scheduler Response)

**Location:** `pkg/frontend/v2/frontend_scheduler_worker.go`

When using V2 frontend with query-scheduler, the scheduler manages the request queue. If the scheduler's queue for a tenant is full, it responds with `TOO_MANY_REQUESTS_PER_TENANT` status.

**Flow:**
1. Frontend worker sends request to scheduler via gRPC
2. Scheduler checks tenant queue size in `queue_broker.go`
3. If queue is full, scheduler responds with `schedulerpb.TOO_MANY_REQUESTS_PER_TENANT`
4. Frontend worker constructs HTTP 429 response

**Trigger Point (lines 457-466):**
```go
func (w *frontendSchedulerWorker) enqueueRequest(...) error {
    // ...
    switch resp.Status {
    case schedulerpb.TOO_MANY_REQUESTS_PER_TENANT:
        level.Warn(spanLogger).Log("msg", "scheduler reported it has too many outstanding requests")
        req.enqueue <- enqueueResult{status: waitForResponse}
        req.response <- queryResultWithBody{
            queryResult: &frontendv2pb.QueryResultRequest{
                HttpResponse: &httpgrpc.HTTPResponse{
                    Code: http.StatusTooManyRequests,
                    Body: []byte("too many outstanding requests"),
                },
            }}
    // ...
    }
}
```

**Configuration:**
```
-query-scheduler.max-outstanding-requests-per-tenant (default: 100)
```

---

### 3. Query Limiter Middleware

**Location:** `pkg/frontend/querymiddleware/query_limiter.go`

The query limiter middleware allows administrators to rate-limit specific queries. When a query matches a configured pattern and is run more frequently than allowed, it returns HTTP 429.

**Flow:**
1. Request passes through `queryLimiterMiddleware.Do()`
2. Middleware checks if query matches any `LimitedQueries` patterns
3. If match found, attempts to add to cache with TTL = `AllowedFrequency`
4. If cache returns `ErrNotStored` (key already exists), query is rate-limited

**Error Construction (`querymiddleware/errors.go:36-41`):**
```go
func newQueryLimitedError(allowedFrequency time.Duration, tenantID string) error {
    return apierror.New(
        apierror.TypeTooManyRequests, globalerror.QueryLimited.Message(
            fmt.Sprintf("the query has been limited by the cluster administrator, and is being run more frequently than the allowed frequency %s against tenant %s", allowedFrequency, tenantID),
        ))
}
```

**Trigger Point (lines 82-88):**
```go
func (ql *queryLimiterMiddleware) Do(ctx context.Context, req MetricsQueryRequest) (Response, error) {
    // ...
    if limitedQueryToEnforce.Query != "" {
        if err := ql.cache.Add(ctx, hashedKey, []byte{}, limitedQueryToEnforce.AllowedFrequency); err != nil {
            if errors.Is(err, cache.ErrNotStored) {
                ql.blockedQueriesCounter.WithLabelValues(tenantMinAllowedFrequency, "limited").Inc()
                return nil, newQueryLimitedError(limitedQueryToEnforce.AllowedFrequency, tenantMinAllowedFrequency)
            }
        }
    }
    // ...
}
```

**Configuration:**
Per-tenant `limited_queries` runtime configuration allows specifying exact query strings and their minimum allowed execution frequency.

---

### 4. Codec Response Decoding

**Location:** `pkg/frontend/querymiddleware/codec.go`

When decoding responses from downstream (querier/scheduler), the codec interprets and passes through HTTP 429 status codes:

```go
if contentType == "" {
    switch r.StatusCode {
    case http.StatusTooManyRequests:
        return nil, apierror.New(apierror.TypeTooManyRequests, string(buf))
    // ...
    }
}
```

---

### 429 Errors Are Non-Retryable

Per `pkg/api/error/error.go`, HTTP 429 errors are **NOT retried** automatically:

```go
func IsRetryableAPIErrorType(typ Type) bool {
    // TypeTooManyRequests we presume a retry of the same request will fail in the same way.
    return typ == TypeInternal
}
```

Only `TypeInternal` (HTTP 500) errors trigger automatic retries.

---

## HTTP 499 (Client Closed Request)

HTTP 499 is a non-standard status code (originated by nginx) indicating the client closed the connection before the server could send a response.

### Source

**Location:** `pkg/frontend/transport/handler.go`

**Constant Definition (line 42):**
```go
const StatusClientClosedRequest = 499
```

**Error Definition (line 64):**
```go
var errCanceled = httpgrpc.Error(StatusClientClosedRequest, context.Canceled.Error())
```

**Conversion Logic (`writeError` function, lines 491-527):**
```go
func writeError(w http.ResponseWriter, err error) int {
    switch {
    case errors.Is(err, context.Canceled):
        err = errCanceled  // HTTP 499
    case errors.Is(err, context.DeadlineExceeded):
        err = errDeadlineExceeded  // HTTP 504
    // ...
    }
    // Write HTTP response with appropriate status code
}
```

### Handler ServeHTTP Flow

```go
func (f *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    // ...
    resp, err := f.roundTripper.RoundTrip(r)
    // ...
    if err != nil {
        statusCode := writeError(w, err)  // May return 499 if context.Canceled
        f.reportQueryStats(r, params, startTime, queryResponseTime, 0, queryDetails, statusCode, err)
        return
    }
    // ...
}
```

### When 499 Occurs

- Client disconnects before query completes (browser tab closed, curl interrupted, etc.)
- Client-side timeout is shorter than query execution time
- Network issues cause connection drop
- Load balancer terminates the connection

### Query Stats Logging

The handler logs canceled requests with a special status:

```go
func (f *Handler) reportQueryStats(...) {
    // ...
    if queryErr != nil {
        logStatus := "failed"
        if errors.Is(queryErr, context.Canceled) {
            logStatus = "canceled"
        } else if errors.Is(queryErr, context.DeadlineExceeded) {
            logStatus = "timeout"
        }
        // ...
    }
}
```

When a 499 response is returned, the query statistics log records the status as "canceled", helping operators distinguish client cancellations from server-side errors.

---

## Configuration Summary

| Parameter | Default | Description |
|-----------|---------|-------------|
| `-querier.max-outstanding-requests-per-tenant` | 100 | Max queued requests per tenant in Frontend V1 |
| `-query-scheduler.max-outstanding-requests-per-tenant` | 100 | Max queued requests per tenant in Query Scheduler |
| `-query-frontend.querier-forget-delay` | 0 | Delay before removing disconnected queriers from tenant shards |
| `-query-frontend.log-queries-longer-than` | 0 | Log queries slower than this duration |
| `-query-frontend.max-body-size` | 10MB | Maximum request body size |
| `-query-frontend.query-stats-enabled` | true | Enable query statistics logging |
| `limited_queries` (runtime config) | - | Per-tenant query rate limiting patterns |

---

## Metrics

Relevant metrics for monitoring 429/499 errors:

| Metric | Description |
|--------|-------------|
| `cortex_query_frontend_queue_length` | Current queue length per tenant (V1) |
| `cortex_query_frontend_discarded_requests_total` | Requests discarded due to queue overflow |
| `cortex_query_frontend_queries_in_progress` | Number of queries currently being processed (V2) |
| `cortex_query_frontend_enqueue_duration_seconds` | Time spent by requests waiting to join the queue or be rejected |

Query stats logs include `status` field with values: `success`, `failed`, `canceled`, `timeout`

---

## Related Files

| File | Description |
|------|-------------|
| `pkg/frontend/config.go` | Combined frontend configuration, mode selection logic |
| `pkg/frontend/v1/frontend.go` | V1 frontend with internal request queue |
| `pkg/frontend/v2/frontend.go` | V2 frontend with scheduler integration |
| `pkg/frontend/v2/frontend_scheduler_worker.go` | Worker handling scheduler communication |
| `pkg/frontend/transport/handler.go` | HTTP handler, error writing, query stats |
| `pkg/frontend/querymiddleware/query_limiter.go` | Query rate limiting middleware |
| `pkg/frontend/querymiddleware/errors.go` | Error construction functions |
| `pkg/frontend/querymiddleware/codec.go` | Request/response encoding/decoding |
| `pkg/scheduler/queue/queue.go` | Request queue implementation (`ErrTooManyRequests`) |
| `pkg/scheduler/queue/queue_broker.go` | Tenant queue size check |
| `pkg/scheduler/scheduler.go` | Handles `TOO_MANY_REQUESTS_PER_TENANT` |
| `pkg/api/error/error.go` | `TypeTooManyRequests`, retry logic |

---

## See Also

- `pkg/querier/CLAUDE.md` - Querier implementation
- `pkg/scheduler/CLAUDE.md` - Query scheduler implementation
