package obs

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrometheusExposition(t *testing.T) {
	r := New()
	r.Counter("runs_total", "Runs by status", Labels{"status": "done"}).Add(2)
	r.Counter("runs_total", "Runs by status", Labels{"status": "failed"}).Inc()
	r.Gauge("active_runs", "Active runs", nil).Set(3)
	r.Observe("run_seconds", "Run duration", nil, 1.5)

	var buf bytes.Buffer
	r.WritePrometheus(&buf)
	out := buf.String()

	for _, want := range []string{
		`runs_total{status="done"} 2`,
		`runs_total{status="failed"} 1`,
		`# TYPE runs_total counter`,
		`active_runs 3`,
		`# TYPE active_runs gauge`,
		`run_seconds_bucket{le="+Inf"} 1`,
		`run_seconds_count 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics output missing %q\n%s", want, out)
		}
	}
}
