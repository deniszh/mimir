// SPDX-License-Identifier: AGPL-3.0-only

package ingest

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/twmb/franz-go/pkg/kgo"
)

func TestKafkaClientExtendedMetrics_OnBrokerRead(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewKafkaClientExtendedMetrics(reg)

	// Simulate reads from two different brokers
	m.OnBrokerRead(kgo.BrokerMetadata{NodeID: 1}, 0, 1000, 0, 0, nil)
	m.OnBrokerRead(kgo.BrokerMetadata{NodeID: 1}, 0, 500, 0, 0, nil)
	m.OnBrokerRead(kgo.BrokerMetadata{NodeID: 2}, 0, 2000, 0, 0, nil)

	// Verify broker 1 total
	assert.Equal(t, float64(1500), testutil.ToFloat64(m.brokerReadBytesTotal.WithLabelValues("1")))
	// Verify broker 2 total
	assert.Equal(t, float64(2000), testutil.ToFloat64(m.brokerReadBytesTotal.WithLabelValues("2")))
}

func TestKafkaClientExtendedMetrics_OnBrokerWrite(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewKafkaClientExtendedMetrics(reg)

	// Simulate writes to two different brokers
	m.OnBrokerWrite(kgo.BrokerMetadata{NodeID: 1}, 0, 100, 0, 0, nil)
	m.OnBrokerWrite(kgo.BrokerMetadata{NodeID: 1}, 0, 200, 0, 0, nil)
	m.OnBrokerWrite(kgo.BrokerMetadata{NodeID: 3}, 0, 300, 0, 0, nil)

	// Verify broker 1 total
	assert.Equal(t, float64(300), testutil.ToFloat64(m.brokerWriteBytesTotal.WithLabelValues("1")))
	// Verify broker 3 total
	assert.Equal(t, float64(300), testutil.ToFloat64(m.brokerWriteBytesTotal.WithLabelValues("3")))
}

func TestKafkaClientExtendedMetrics_OnBrokerE2E(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewKafkaClientExtendedMetrics(reg)

	e2e := kgo.BrokerE2E{
		WriteWait:   100 * time.Millisecond,
		TimeToWrite: 50 * time.Millisecond,
		ReadWait:    200 * time.Millisecond,
		TimeToRead:  75 * time.Millisecond,
	}

	m.OnBrokerE2E(kgo.BrokerMetadata{NodeID: 1}, 0, e2e)

	// Verify histograms have observations (count should be 1)
	assert.Equal(t, 1, testutil.CollectAndCount(m.writeWaitSeconds))
	assert.Equal(t, 1, testutil.CollectAndCount(m.writeTimeSeconds))
	assert.Equal(t, 1, testutil.CollectAndCount(m.readWaitSeconds))
	assert.Equal(t, 1, testutil.CollectAndCount(m.readTimeSeconds))
	assert.Equal(t, 1, testutil.CollectAndCount(m.requestDurationE2ESeconds))
}

func TestKafkaClientExtendedMetrics_OnBrokerThrottle(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewKafkaClientExtendedMetrics(reg)

	m.OnBrokerThrottle(kgo.BrokerMetadata{NodeID: 1}, 500*time.Millisecond, true)

	// Verify histogram has observation
	assert.Equal(t, 1, testutil.CollectAndCount(m.requestThrottledSeconds))
}

func TestKafkaClientExtendedMetrics_NegativeNodeID(t *testing.T) {
	// Seed brokers have negative node IDs, verify we handle them correctly
	reg := prometheus.NewRegistry()
	m := NewKafkaClientExtendedMetrics(reg)

	// Seed brokers typically have very negative IDs like -1
	m.OnBrokerRead(kgo.BrokerMetadata{NodeID: -1}, 0, 1000, 0, 0, nil)
	m.OnBrokerWrite(kgo.BrokerMetadata{NodeID: -1}, 0, 500, 0, 0, nil)

	// Verify the metrics are recorded with negative node ID as string
	assert.Equal(t, float64(1000), testutil.ToFloat64(m.brokerReadBytesTotal.WithLabelValues("-1")))
	assert.Equal(t, float64(500), testutil.ToFloat64(m.brokerWriteBytesTotal.WithLabelValues("-1")))
}
