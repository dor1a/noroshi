// Package metrics exposes check and component state to Prometheus. Only the
// prober leader sets check metrics; other instances report leader=0.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/dor1a/noroshi/internal/judge"
)

// Metrics holds the registry and every noroshi series.
type Metrics struct {
	reg *prometheus.Registry

	checkUp       *prometheus.GaugeVec
	checkDuration *prometheus.GaugeVec
	checkLastRun  *prometheus.GaugeVec
	checkInterval *prometheus.GaugeVec
	certExpiry    *prometheus.GaugeVec
	domainExpiry  *prometheus.GaugeVec
	component     *prometheus.GaugeVec
	leader        prometheus.Gauge
}

// CheckLabels identifies a check in metrics.
type CheckLabels struct {
	Page, Component, Check, Type string
	Alert                        bool
}

// New registers all collectors.
func New(version string) *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		reg: reg,
		checkUp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "noroshi_check_up",
			Help: "Debounced check state: 1 up, 0 down. Absent while unknown. alert=\"false\" marks checks that should not page.",
		}, []string{"page", "component", "check", "type", "alert"}),
		checkDuration: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "noroshi_check_duration_seconds",
			Help: "Duration of the last probe run.",
		}, []string{"page", "component", "check"}),
		checkLastRun: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "noroshi_check_last_run_timestamp_seconds",
			Help: "Unix time of the last probe run.",
		}, []string{"page", "component", "check"}),
		checkInterval: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "noroshi_check_interval_seconds",
			Help: "Configured interval between probe runs.",
		}, []string{"page", "component", "check"}),
		certExpiry: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "noroshi_cert_expiry_timestamp_seconds",
			Help: "Unix time when the presented certificate expires.",
		}, []string{"page", "component", "check", "alert"}),
		domainExpiry: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "noroshi_domain_expiry_timestamp_seconds",
			Help: "Unix time when the domain registration expires.",
		}, []string{"page", "component", "check", "alert"}),
		component: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "noroshi_component_status",
			Help: "1 for the component's current status, 0 for the others.",
		}, []string{"page", "component", "status"}),
		leader: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "noroshi_prober_leader",
			Help: "1 if this instance runs the checks.",
		}),
	}
	build := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "noroshi_build_info",
		Help: "Build information.",
	}, []string{"version"})
	build.WithLabelValues(version).Set(1)

	reg.MustRegister(
		m.checkUp, m.checkDuration, m.checkLastRun, m.checkInterval,
		m.certExpiry, m.domainExpiry, m.component, m.leader, build,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return m
}

// Handler serves /metrics.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// SetLeader records whether this instance runs the checks. Losing the lead
// drops the check series so a follower does not report stale values.
func (m *Metrics) SetLeader(on bool) {
	if on {
		m.leader.Set(1)
		return
	}
	m.leader.Set(0)
	for _, v := range []*prometheus.GaugeVec{
		m.checkUp, m.checkDuration, m.checkLastRun, m.checkInterval,
		m.certExpiry, m.domainExpiry, m.component,
	} {
		v.Reset()
	}
}

// CheckRun records one probe run and the resulting state.
func (m *Metrics) CheckRun(l CheckLabels, state judge.CheckState, at time.Time, dur, interval time.Duration, cert, domain *time.Time) {
	short := []string{l.Page, l.Component, l.Check}
	alert := strconv.FormatBool(l.Alert)
	switch state {
	case judge.Up:
		m.checkUp.WithLabelValues(l.Page, l.Component, l.Check, l.Type, alert).Set(1)
	case judge.Down:
		m.checkUp.WithLabelValues(l.Page, l.Component, l.Check, l.Type, alert).Set(0)
	default:
		m.checkUp.DeleteLabelValues(l.Page, l.Component, l.Check, l.Type, alert)
	}
	m.checkDuration.WithLabelValues(short...).Set(dur.Seconds())
	m.checkLastRun.WithLabelValues(short...).Set(float64(at.Unix()))
	m.checkInterval.WithLabelValues(short...).Set(interval.Seconds())
	if cert != nil {
		m.certExpiry.WithLabelValues(l.Page, l.Component, l.Check, alert).Set(float64(cert.Unix()))
	}
	if domain != nil {
		m.domainExpiry.WithLabelValues(l.Page, l.Component, l.Check, alert).Set(float64(domain.Unix()))
	}
}

// ComponentStatus sets the status series for one component.
func (m *Metrics) ComponentStatus(page, component string, status judge.Status) {
	for _, s := range judge.AllStatuses {
		v := 0.0
		if s == status {
			v = 1
		}
		m.component.WithLabelValues(page, component, string(s)).Set(v)
	}
}
