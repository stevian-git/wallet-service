package telemetry

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func getCounterValue(cv *prometheus.CounterVec, labels ...string) float64 {
	counter, err := cv.GetMetricWithLabelValues(labels...)
	if err != nil {
		return 0
	}
	m := &dto.Metric{}
	if err := counter.(prometheus.Metric).Write(m); err != nil {
		return 0
	}
	return m.Counter.GetValue()
}

func TestRecordTransfer(t *testing.T) {
	before := getCounterValue(transfersTotal, "topup", "posted")
	RecordTransfer("topup", "posted")
	after := getCounterValue(transfersTotal, "topup", "posted")
	if after-before != 1 {
		t.Errorf("expected counter to increment by 1, got %v", after-before)
	}
}

func TestRecordHTTP(t *testing.T) {
	before := getCounterValue(httpRequestsTotal, "POST", "/v1/topups", "200")
	RecordHTTP("POST", "/v1/topups", "200", 100*time.Millisecond)
	after := getCounterValue(httpRequestsTotal, "POST", "/v1/topups", "200")
	if after-before != 1 {
		t.Errorf("expected counter to increment by 1, got %v", after-before)
	}
}

func TestRecordReplay(t *testing.T) {
	before := getCounterValue(idempotencyReplays, "/v1/topups")
	RecordReplay("/v1/topups")
	after := getCounterValue(idempotencyReplays, "/v1/topups")
	if after-before != 1 {
		t.Errorf("expected counter to increment by 1, got %v", after-before)
	}
}
