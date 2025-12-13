package waypoint

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPreferIPv4(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []string
	}{
		{
			name:     "empty input",
			input:    []string{},
			expected: nil,
		},
		{
			name:     "only IPv4",
			input:    []string{"10.0.0.1", "192.168.1.1", "172.16.0.1"},
			expected: []string{"10.0.0.1", "192.168.1.1", "172.16.0.1"},
		},
		{
			name:     "only IPv6 - should return IPv6 for IPv6-only clusters",
			input:    []string{"2001:2::1", "fe80::1", "::1"},
			expected: []string{"2001:2::1", "fe80::1", "::1"},
		},
		{
			name:     "mixed IPv4 and IPv6 - should prefer IPv4",
			input:    []string{"10.0.0.1", "2001:2::1", "192.168.1.1", "fe80::1"},
			expected: []string{"10.0.0.1", "192.168.1.1"},
		},
		{
			name:     "auto-allocated addresses from ServiceEntry - prefer IPv4",
			input:    []string{"240.240.0.1", "2001:2::c"},
			expected: []string{"240.240.0.1"},
		},
		{
			name:     "invalid addresses filtered out",
			input:    []string{"10.0.0.1", "not-an-ip", "192.168.1.1"},
			expected: []string{"10.0.0.1", "192.168.1.1"},
		},
		{
			name:     "only invalid addresses",
			input:    []string{"not-an-ip", "also-not-valid"},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := preferIPv4(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
