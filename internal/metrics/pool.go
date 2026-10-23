package metrics

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// RegisterPool exports the connection pool statistics of pool.
func RegisterPool(reg prometheus.Registerer, pool *pgxpool.Pool) {
	reg.MustRegister(&poolCollector{pool: pool})
}

var (
	poolAcquired = prometheus.NewDesc(namespace+"_db_pool_acquired_conns", "Connections currently in use.", nil, nil)
	poolIdle     = prometheus.NewDesc(namespace+"_db_pool_idle_conns", "Idle connections.", nil, nil)
	poolTotal    = prometheus.NewDesc(namespace+"_db_pool_total_conns", "Open connections.", nil, nil)
	poolMax      = prometheus.NewDesc(namespace+"_db_pool_max_conns", "Maximum pool size.", nil, nil)
	poolWait     = prometheus.NewDesc(namespace+"_db_pool_empty_acquire_wait_seconds_total",
		"Time spent waiting for a connection because the pool was exhausted.", nil, nil)
)

type poolCollector struct{ pool *pgxpool.Pool }

func (c *poolCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{poolAcquired, poolIdle, poolTotal, poolMax, poolWait} {
		ch <- d
	}
}

func (c *poolCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.pool.Stat()
	ch <- prometheus.MustNewConstMetric(poolAcquired, prometheus.GaugeValue, float64(s.AcquiredConns()))
	ch <- prometheus.MustNewConstMetric(poolIdle, prometheus.GaugeValue, float64(s.IdleConns()))
	ch <- prometheus.MustNewConstMetric(poolTotal, prometheus.GaugeValue, float64(s.TotalConns()))
	ch <- prometheus.MustNewConstMetric(poolMax, prometheus.GaugeValue, float64(s.MaxConns()))
	ch <- prometheus.MustNewConstMetric(poolWait, prometheus.CounterValue, s.EmptyAcquireWaitTime().Seconds())
}
