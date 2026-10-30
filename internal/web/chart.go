package web

import (
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/dor1a/noroshi/internal/i18n"
	"github.com/dor1a/noroshi/internal/store"
)

// chartBuckets is the most slices the latency chart cuts a period into.
const chartBuckets = 240

// chartSlices is how many slices a period gets: chartBuckets, or fewer when
// that would make a slice shorter than the check interval and leave most of
// them empty.
func chartSlices(period, interval time.Duration) int {
	n := chartBuckets
	if interval > 0 {
		if fit := int(period / interval); fit < n {
			n = fit
		}
	}
	return max(n, 1)
}

// Chart geometry in viewBox units. The SVG scales to the page width.
const (
	chartW, chartH       = 960, 150
	chartLeft, chartTop  = 52, 8
	chartRight, chartBot = 8, 22
)

// latencyChart draws a check's latency over a period as inline SVG: the
// average of passing results as a line, the highest as a faint line, slices
// with failures as red columns, and the amber and red latency limits. Each
// slice has a native tooltip. It returns "" when there is nothing to draw.
func latencyChart(locale string, buckets []store.LatencyBucket, n int, from, to time.Time, warn, bad time.Duration) template.HTML {
	if len(buckets) == 0 || n <= 0 {
		return ""
	}
	plotW := float64(chartW - chartLeft - chartRight)
	plotH := float64(chartH - chartTop - chartBot)
	top := 0
	for _, b := range buckets {
		if b.MaxMS != nil && *b.MaxMS > top {
			top = *b.MaxMS
		}
	}
	warnMS, badMS := int(warn/time.Millisecond), int(bad/time.Millisecond)
	if top < warnMS {
		top = warnMS
	}
	if top < 10 {
		top = 10
	}
	top = top * 11 / 10
	slot := plotW / float64(n)
	x := func(i int) float64 { return float64(chartLeft) + (float64(i)+0.5)*slot }
	y := func(ms float64) float64 { return float64(chartTop) + plotH - ms/float64(top)*plotH }
	stride := to.Sub(from) / time.Duration(n)

	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="lat-chart" viewBox="0 0 %d %d" role="img" aria-label="%s">`, chartW, chartH, template.HTMLEscapeString(tf(locale, "admin.latency_chart")))
	// Axes and limits.
	fmt.Fprintf(&b, `<line class="lat-axis" x1="%d" y1="%.1f" x2="%d" y2="%.1f"/>`, chartLeft, y(0), chartW-chartRight, y(0))
	for _, l := range []struct {
		ms    int
		class string
	}{{warnMS, "lat-warn"}, {badMS, "lat-bad"}} {
		if l.ms > 0 && l.ms < top {
			fmt.Fprintf(&b, `<line class="%s" x1="%d" y1="%.1f" x2="%d" y2="%.1f"/>`, l.class, chartLeft, y(float64(l.ms)), chartW-chartRight, y(float64(l.ms)))
		}
	}
	fmt.Fprintf(&b, `<text class="lat-label" x="%d" y="%.1f" text-anchor="end">%d ms</text>`, chartLeft-6, y(float64(top))+10, top)
	fmt.Fprintf(&b, `<text class="lat-label" x="%d" y="%.1f" text-anchor="end">0</text>`, chartLeft-6, y(0))
	fmt.Fprintf(&b, `<text class="lat-label" x="%d" y="%d">%s</text>`, chartLeft, chartH-6, from.Local().Format("01-02 15:04"))
	fmt.Fprintf(&b, `<text class="lat-label" x="%d" y="%d" text-anchor="end">%s</text>`, chartW-chartRight, chartH-6, to.Local().Format("01-02 15:04"))

	// Failures behind the lines, then the highest and average latency.
	for _, bk := range buckets {
		if bk.Failures > 0 {
			fmt.Fprintf(&b, `<rect class="lat-fail" x="%.1f" y="%d" width="%.1f" height="%.1f"/>`, x(bk.Index)-slot/2, chartTop, slot, plotH)
		}
	}
	type point struct{ x, y float64 }
	line := func(class string, val func(store.LatencyBucket) (float64, bool)) {
		var pts []point
		flush := func() {
			switch len(pts) {
			case 0:
			case 1:
				fmt.Fprintf(&b, `<circle class="%s" cx="%.1f" cy="%.1f" r="1.5"/>`, class, pts[0].x, pts[0].y)
			default:
				parts := make([]string, len(pts))
				for i, p := range pts {
					parts[i] = fmt.Sprintf("%.1f,%.1f", p.x, p.y)
				}
				fmt.Fprintf(&b, `<polyline class="%s" points="%s"/>`, class, strings.Join(parts, " "))
			}
			pts = pts[:0]
		}
		prev := -2
		for _, bk := range buckets {
			v, ok := val(bk)
			// Leave a gap where passing results stop for more than one slice.
			if !ok || bk.Index > prev+2 {
				flush()
			}
			if ok {
				pts = append(pts, point{x(bk.Index), y(v)})
			}
			prev = bk.Index
		}
		flush()
	}
	line("lat-max", func(bk store.LatencyBucket) (float64, bool) {
		if bk.MaxMS == nil {
			return 0, false
		}
		return float64(*bk.MaxMS), true
	})
	line("lat-avg", func(bk store.LatencyBucket) (float64, bool) {
		if bk.AvgMS == nil {
			return 0, false
		}
		return *bk.AvgMS, true
	})

	// Tooltip targets, one per slice with results.
	for _, bk := range buckets {
		start := from.Add(time.Duration(bk.Index) * stride)
		tip := start.Local().Format("01-02 15:04") + "–" + start.Add(stride).Local().Format("15:04")
		if bk.AvgMS != nil {
			tip += fmt.Sprintf(" · %s %.0f ms · %s %d ms", tf(locale, "admin.lat_avg"), *bk.AvgMS, tf(locale, "admin.lat_max"), *bk.MaxMS)
		}
		if bk.Failures > 0 {
			tip += " · " + tf(locale, "admin.lat_fails", bk.Failures, bk.Count)
		}
		fmt.Fprintf(&b, `<rect class="lat-hit" x="%.1f" y="%d" width="%.1f" height="%.1f"><title>%s</title></rect>`,
			x(bk.Index)-slot/2, chartTop, slot, plotH, template.HTMLEscapeString(tip))
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

// tf is the translated format string with its arguments filled in.
func tf(locale, key string, args ...any) string {
	return fmt.Sprintf(i18n.T(locale, key), args...)
}
