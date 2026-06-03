package telemetry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewTracer(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		endpoint    string
		expectError bool
		expectNoop  bool
	}{
		{
			name:        "empty endpoint returns noop tracer",
			endpoint:    "",
			expectError: false,
			expectNoop:  true,
		},
		{
			name:        "invalid endpoint returns error",
			endpoint:    "://malformed",
			expectError: true,
			expectNoop:  false,
		},
		{
			name:        "valid http endpoint returns real tracer",
			endpoint:    "http://localhost:4318",
			expectError: false,
			expectNoop:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tracer, shutdown, err := NewTracer(tc.endpoint)

			if tc.expectError {
				require.Error(t, err)
				assert.Nil(t, tracer)
				assert.Nil(t, shutdown)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, tracer)
			require.NotNil(t, shutdown)

			_, span := tracer.Start(context.Background(), "test")
			defer span.End()

			if tc.expectNoop {
				assert.False(t, span.SpanContext().HasSpanID(), "noop span should not have a span id")
			} else {
				assert.True(t, span.SpanContext().HasSpanID(), "real span should have a span id")
			}

			require.NoError(t, shutdown(context.Background()))
		})
	}
}
