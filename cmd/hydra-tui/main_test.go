package main

import "testing"

func TestDaemonAddressFromURL(t *testing.T) {
	tests := []struct {
		url    string
		want   string
		wantOK bool
	}{
		{"http://localhost:9000", "localhost:9000", true},
		{"http://127.0.0.1:9000", "127.0.0.1:9000", true},
		{"http://[::1]:9000", "[::1]:9000", true},
		{"http://localhost", "localhost:80", true},
		{"https://localhost", "localhost:443", true},
		{"https://example.com:9000", "", false},
		{"http://192.168.1.50:9000", "", false},
		{"not-a-url", "", false},
	}

	for _, tt := range tests {
		got, ok := daemonAddressFromURL(tt.url)
		if ok != tt.wantOK || got != tt.want {
			t.Errorf("daemonAddressFromURL(%q) = (%q, %v), want (%q, %v)",
				tt.url, got, ok, tt.want, tt.wantOK)
		}
	}
}

