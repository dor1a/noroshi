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

	pageInfo      *prometheus.GaugeVec
	checkUp       *prometheus.GaugeVec
	checkInfo     *prometheus.GaugeVec
	checkFailure  *prometheus.GaugeVec
	checkDuration *prometheus.GaugeVec
	checkLastRun  *prometheus.GaugeVec
	checkInterval *prometheus.GaugeVec
	certExpiry    *prometheus.GaugeVec
	domainExpiry  *prometheus.GaugeVec
	component     *prometheus.GaugeVec
	leader        prometheus.Gauge
}

// CheckLabels identifies a check in metrics. Name and Target only go on
// noroshi_check_info, so alert rules can show them without joining.
type CheckLabels struct {
	Page, Component, Check, Type string
	Alert                        bool
	Name, Target                 string
}

// New registers all collectors.
func New(version string) *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		reg: reg,
		pageInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "noroshi_page_info",
			Help: "Always 1. The page's title and address, for alert messages. Every instance reports it.",
		}, []string{"page", "title", "url"}),
		checkUp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "noroshi_check_up",
			Help: "Debounced check state: 1 up, 0 down. Absent while unknown. alert=\"false\" marks checks that should not page.",
		}, []string{"page", "component", "check", "type", "alert"}),
		checkInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "noroshi_check_info",
			Help: "Always 1. Carries the component's display name and what the check probes.",
		}, []string{"page", "component", "check", "name", "target"}),
		checkFailure: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "noroshi_check_failure_info",
			Help: "Always 1. Reason of the latest failed probe, kept until the check is up again.",
		}, []string{"page", "component", "check", "reason"}),
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
		m.pageInfo, m.checkUp, m.checkInfo, m.checkFailure, m.checkDuration, m.checkLastRun, m.checkInterval,
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
		m.checkUp, m.checkInfo, m.checkFailure, m.checkDuration, m.checkLastRun, m.checkInterval,
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
	m.checkInfo.WithLabelValues(l.Page, l.Component, l.Check, l.Name, l.Target).Set(1)
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

// PageInfo records a page's title and address. It comes from the config,
// not from the prober, so it survives a change of leader.
func (m *Metrics) PageInfo(page, title, url string) {
	m.pageInfo.WithLabelValues(page, title, url).Set(1)
}

// CheckFailure replaces the failure reason of a check. An empty reason
// removes it.
func (m *Metrics) CheckFailure(l CheckLabels, reason string) {
	m.checkFailure.DeletePartialMatch(prometheus.Labels{"page": l.Page, "component": l.Component, "check": l.Check})
	if reason != "" {
		m.checkFailure.WithLabelValues(l.Page, l.Component, l.Check, reason).Set(1)
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
