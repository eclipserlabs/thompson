package thompson

import (
	"sync/atomic"
)

// PrometheusMetrics is a pull-based Counter/Gauge set for PolicyObserver.
// Attach via Policy.SetObserver. In prod, wire to prometheus/client_golang.
type PrometheusMetrics struct {
	selectsTotal   atomic.Uint64
	recordsTotal   atomic.Uint64
	discountsTotal atomic.Uint64
}

func NewPrometheusMetrics() *PrometheusMetrics { return &PrometheusMetrics{} }

func (m *PrometheusMetrics) OnSelect(chosen string, scores map[string]float64) {
	m.selectsTotal.Add(1)
	_ = chosen
	_ = scores
}
func (m *PrometheusMetrics) OnRecord(arm string, reward float64, p Posterior) { m.recordsTotal.Add(1) }
func (m *PrometheusMetrics) OnArmAdded(id string, warmStarted bool)           {}
func (m *PrometheusMetrics) OnDiscount(factor float64)                        { m.discountsTotal.Add(1) }

func (m *PrometheusMetrics) SelectsTotal() uint64   { return m.selectsTotal.Load() }
func (m *PrometheusMetrics) RecordsTotal() uint64   { return m.recordsTotal.Load() }
func (m *PrometheusMetrics) DiscountsTotal() uint64 { return m.discountsTotal.Load() }
