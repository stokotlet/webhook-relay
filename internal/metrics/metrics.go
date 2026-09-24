package metrics

import "github.com/prometheus/client_golang/prometheus"

type Metrics struct {
	Attempts *prometheus.CounterVec
	Duration prometheus.Histogram
	Active   prometheus.Gauge
}

func New(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Attempts: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "relay_delivery_attempts_total", Help: "Completed delivery attempts by resulting state."}, []string{"result"}),
		Duration: prometheus.NewHistogram(prometheus.HistogramOpts{Name: "relay_delivery_duration_seconds", Help: "Outbound HTTP request duration.", Buckets: prometheus.DefBuckets}),
		Active:   prometheus.NewGauge(prometheus.GaugeOpts{Name: "relay_active_workers", Help: "Workers currently processing a delivery."}),
	}
	reg.MustRegister(m.Attempts, m.Duration, m.Active)
	return m
}
