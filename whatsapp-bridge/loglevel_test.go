package main

import "testing"

func TestBridgeLogLevel(t *testing.T) {
	cases := []struct {
		raw, want string
		ok        bool
	}{
		{"", "DEBUG", true},
		{" info ", "INFO", true},
		{"warn", "WARN", true},
		{"ERROR", "ERROR", true},
		{"verbose", "DEBUG", false},
	}
	for _, c := range cases {
		got, ok := bridgeLogLevel(c.raw)
		if got != c.want || ok != c.ok {
			t.Errorf("bridgeLogLevel(%q) = %q, %v; want %q, %v", c.raw, got, ok, c.want, c.ok)
		}
	}
}
