package retention

import (
	"testing"
	"time"
)

func TestParseWindowRequiresCanonicalSupportedDuration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		value   string
		want    time.Duration
		wantErr bool
	}{
		{name: "minimum", value: "1m0s", want: time.Minute},
		{name: "ordinary", value: "30m0s", want: 30 * time.Minute},
		{name: "maximum", value: "720h0m0s", want: 30 * 24 * time.Hour},
		{name: "missing", wantErr: true},
		{name: "malformed", value: "later", wantErr: true},
		{name: "noncanonical", value: "1800s", wantErr: true},
		{name: "too short", value: "59s", wantErr: true},
		{name: "too long", value: "721h0m0s", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			window, err := ParseWindow(test.value)
			if test.wantErr {
				if err == nil {
					t.Fatalf("ParseWindow(%q) unexpectedly succeeded with %q", test.value, window)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			duration, err := window.Duration()
			if err != nil || duration != test.want {
				t.Fatalf("Window(%q).Duration() = %v, %v; want %v", window, duration, err, test.want)
			}
		})
	}
}
