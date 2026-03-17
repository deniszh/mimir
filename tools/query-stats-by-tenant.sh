#!/usr/bin/env bash
#
# query-stats-by-tenant.sh — Ad-hoc per-tenant query stats from Loki
#
# Queries the "query stats" log lines emitted by Mimir query-frontend
# and produces a per-tenant summary table.
#
# Prerequisites:
#   - logcli: go install github.com/grafana/loki/cmd/logcli@latest
#   - jq
#   - LOKI_ADDR environment variable set (e.g. https://loki.example.com)
#   - Optionally LOKI_USERNAME / LOKI_PASSWORD for auth
#
# Usage:
#   ./query-stats-by-tenant.sh <cluster> <namespace> [minutes_back] [tenant_regex]
#   ./query-stats-by-tenant.sh <cluster> <namespace> [minutes_back] [tenant_regex] --top-queries N
#
# Examples:
#   ./query-stats-by-tenant.sh prod-us mimir 60
#   ./query-stats-by-tenant.sh prod-us mimir 120 "tenant-abc.*"
#   ./query-stats-by-tenant.sh prod-us mimir 60 ".*" --top-queries 20

set -euo pipefail

CLUSTER="${1:?Usage: $0 <cluster> <namespace> [minutes_back] [tenant_regex] [--top-queries N]}"
NAMESPACE="${2:?Usage: $0 <cluster> <namespace> [minutes_back] [tenant_regex] [--top-queries N]}"
MINUTES="${3:-60}"
TENANT="${4:-.*}"

# Check for --top-queries mode
TOP_QUERIES=0
shift 4 2>/dev/null || true
while [[ $# -gt 0 ]]; do
    case "$1" in
        --top-queries)
            TOP_QUERIES="${2:?--top-queries requires a number}"
            shift 2
            ;;
        *)
            echo "Unknown option: $1" >&2
            exit 1
            ;;
    esac
done

# Validate prerequisites
for cmd in logcli jq awk; do
    if ! command -v "$cmd" &>/dev/null; then
        echo "Error: $cmd is required but not found in PATH" >&2
        exit 1
    fi
done

if [[ -z "${LOKI_ADDR:-}" ]]; then
    echo "Error: LOKI_ADDR environment variable must be set (e.g. https://loki.example.com)" >&2
    exit 1
fi

QUERY="{cluster=\"${CLUSTER}\", namespace=\"${NAMESPACE}\", container=~\"query-frontend.*\"} |= \"query stats\" | logfmt | user=~\"${TENANT}\""

echo "Querying Loki at ${LOKI_ADDR}..." >&2
echo "  Cluster:   ${CLUSTER}" >&2
echo "  Namespace: ${NAMESPACE}" >&2
echo "  Lookback:  ${MINUTES}m" >&2
echo "  Tenant:    ${TENANT}" >&2
echo "" >&2

# Fetch logs as JSONL
RAW=$(logcli query \
    --addr="${LOKI_ADDR}" \
    --from="${MINUTES}m" \
    --limit=50000 \
    --output=jsonl \
    --quiet \
    "${QUERY}" 2>/dev/null) || {
    echo "Error: logcli query failed. Check LOKI_ADDR and credentials." >&2
    exit 1
}

if [[ -z "$RAW" ]]; then
    echo "No results found for the given query." >&2
    exit 0
fi

if [[ "$TOP_QUERIES" -gt 0 ]]; then
    # --top-queries mode: show individual heaviest queries
    echo "=== Top ${TOP_QUERIES} Queries by Fetched Series ==="
    echo ""
    printf "%-30s %15s %15s %12s %s\n" "TENANT" "FETCHED_SERIES" "CHUNK_BYTES" "WALL_TIME_S" "QUERY"
    printf "%-30s %15s %15s %12s %s\n" "------" "--------------" "-----------" "-----------" "-----"

    echo "$RAW" \
    | jq -r '
        [
            (.labels.user // .line_labels.user // "unknown"),
            (.labels.fetched_series_count // .line_labels.fetched_series_count // "0"),
            (.labels.fetched_chunk_bytes // .line_labels.fetched_chunk_bytes // "0"),
            (.labels.query_wall_time_seconds // .line_labels.query_wall_time_seconds // "0"),
            (.labels.param_query // .line_labels.param_query // "N/A")
        ] | @tsv
    ' 2>/dev/null \
    | sort -t$'\t' -k2 -rn \
    | head -n "${TOP_QUERIES}" \
    | while IFS=$'\t' read -r user series bytes wall query; do
        # Truncate query to 80 chars for display
        display_query="${query:0:80}"
        [[ ${#query} -gt 80 ]] && display_query="${display_query}..."
        printf "%-30s %15s %15s %12s %s\n" "$user" "$series" "$bytes" "$wall" "$display_query"
    done
else
    # Default mode: per-tenant summary
    echo "=== Per-Tenant Query Summary (last ${MINUTES}m) ==="
    echo ""
    printf "%-30s %8s %15s %15s %12s\n" "TENANT" "QUERIES" "FETCHED_SERIES" "CHUNK_BYTES" "MAX_WALL_S"
    printf "%-30s %8s %15s %15s %12s\n" "------" "-------" "--------------" "-----------" "----------"

    echo "$RAW" \
    | jq -r '
        [
            (.labels.user // .line_labels.user // "unknown"),
            (.labels.fetched_series_count // .line_labels.fetched_series_count // "0"),
            (.labels.fetched_chunk_bytes // .line_labels.fetched_chunk_bytes // "0"),
            (.labels.query_wall_time_seconds // .line_labels.query_wall_time_seconds // "0")
        ] | @tsv
    ' 2>/dev/null \
    | awk -F'\t' '{
        u=$1
        series[u]+=$2+0
        bytes[u]+=$3+0
        count[u]++
        if($4+0 > max_wall[u]+0) max_wall[u]=$4+0
    } END {
        for(u in count)
            printf "%-30s %8d %15d %15d %12.2f\n", u, count[u], series[u], bytes[u], max_wall[u]
    }' \
    | sort -k3 -rn
fi

echo "" >&2
echo "Done." >&2
