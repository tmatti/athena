package oauth

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLimiter(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLimiter(3, 60) // burst 3, one token per second
	l.now = func() time.Time { return now }

	require.True(t, l.allow())
	require.True(t, l.allow())
	require.True(t, l.allow())
	require.False(t, l.allow(), "burst exhausted")

	now = now.Add(time.Second)
	require.True(t, l.allow(), "one token refilled")
	require.False(t, l.allow())

	// Refill is capped at the burst size.
	now = now.Add(time.Hour)
	for i := 0; i < 3; i++ {
		require.True(t, l.allow(), i)
	}
	require.False(t, l.allow())
}
