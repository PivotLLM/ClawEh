package gatewayproto

import "testing"

func TestNegotiateProtocol(t *testing.T) {
	cases := []struct {
		min, max int
		probe    bool
		want     int
	}{
		{3, 3, false, 3}, // Rabbit R1 (firmware 20260619.1) speaks protocol 3
		{4, 4, false, 4}, // current OpenClaw client
		{3, 4, false, 4}, // range -> highest common
		{1, 2, false, 0}, // below our floor (3)
		{5, 5, false, 0}, // above our ceiling (4)
		{2, 5, false, 4}, // wide range clamps to our max
	}
	for _, c := range cases {
		if got := NegotiateProtocol(c.min, c.max, c.probe); got != c.want {
			t.Errorf("NegotiateProtocol(%d,%d,probe=%v)=%d want %d", c.min, c.max, c.probe, got, c.want)
		}
	}
}
