package cmd

import "testing"

func TestIsLoopbackAddr(t *testing.T) {
	tests := []struct {
		addr     string
		loopback bool
	}{
		{"127.0.0.1:8421", true},
		{"127.0.0.2:8421", true},
		{"localhost:8421", true},
		{"[::1]:8421", true},
		{"127.0.0.1:0", true},

		// Empty host binds all interfaces.
		{":8421", false},
		{":0", false},
		{"0.0.0.0:8421", false},
		{"[::]:8421", false},
		{"192.168.1.5:8421", false},
		{"10.0.0.1:8421", false}, // private but still network-reachable
		{"example.com:8421", false},

		// Malformed addresses are treated as non-loopback (fail closed).
		{"8421", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			if got := isLoopbackAddr(tt.addr); got != tt.loopback {
				t.Errorf("isLoopbackAddr(%q) = %v, want %v", tt.addr, got, tt.loopback)
			}
		})
	}
}
