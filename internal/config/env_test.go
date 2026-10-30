package config

import (
	"strings"
	"testing"
	"time"
)

func TestResultRetention(t *testing.T) {
	t.Setenv("NOROSHI_DATABASE_URL", "postgres://x")
	t.Setenv("NOROSHI_DEV_NO_AUTH", "1")
	for _, c := range []struct {
		value string
		want  time.Duration
		err   string
	}{
		{"", DefaultResultRetention, ""},
		{"30d", 30 * 24 * time.Hour, ""},
		{"36h", 36 * time.Hour, ""},
		{"12h", DefaultResultRetention, "at least 1d"},
		{"soon", DefaultResultRetention, "NOROSHI_RESULT_RETENTION"},
	} {
		t.Setenv("NOROSHI_RESULT_RETENTION", c.value)
		e, err := LoadEnv()
		if c.err == "" && err != nil || c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)) {
			t.Errorf("%q: error %v", c.value, err)
		}
		if e.ResultRetention != c.want {
			t.Errorf("%q: retention %s", c.value, e.ResultRetention)
		}
	}
}
