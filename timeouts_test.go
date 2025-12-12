package wadjit

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestHTTPTimeouts_Validate(t *testing.T) {
	t.Run("valid timeouts", func(t *testing.T) {
		timeouts := HTTPTimeouts{
			Total:          10 * time.Second,
			ResponseHeader: 5 * time.Second,
			IdleConn:       30 * time.Second,
			TLSHandshake:   10 * time.Second,
			Dial:           5 * time.Second,
		}
		err := timeouts.Validate()
		assert.NoError(t, err)
	})

	t.Run("zero timeouts are valid", func(t *testing.T) {
		timeouts := HTTPTimeouts{}
		err := timeouts.Validate()
		assert.NoError(t, err)
	})

	t.Run("negative dial timeout is invalid", func(t *testing.T) {
		timeouts := HTTPTimeouts{
			Dial: -1 * time.Second,
		}
		err := timeouts.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "Dial cannot be negative")
	})
}

func TestWSTimeouts_Validate(t *testing.T) {
	t.Run("valid timeouts", func(t *testing.T) {
		timeouts := WSTimeouts{
			Handshake:    5 * time.Second,
			Read:         30 * time.Second,
			Write:        10 * time.Second,
			PingInterval: 20 * time.Second,
		}
		err := timeouts.Validate()
		assert.NoError(t, err)
	})

	t.Run("zero timeouts are valid", func(t *testing.T) {
		timeouts := WSTimeouts{}
		err := timeouts.Validate()
		assert.NoError(t, err)
	})

	t.Run("ping interval greater than or equal to read timeout is invalid", func(t *testing.T) {
		timeouts := WSTimeouts{
			Read:         10 * time.Second,
			PingInterval: 10 * time.Second,
		}
		err := timeouts.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "PingInterval must be less than")

		timeouts.PingInterval = 15 * time.Second
		err = timeouts.Validate()
		assert.Error(t, err)
	})

	t.Run("ping interval less than read timeout is valid", func(t *testing.T) {
		timeouts := WSTimeouts{
			Read:         10 * time.Second,
			PingInterval: 5 * time.Second,
		}
		err := timeouts.Validate()
		assert.NoError(t, err)
	})

	t.Run("ping interval without read timeout is valid", func(t *testing.T) {
		timeouts := WSTimeouts{
			PingInterval: 10 * time.Second,
		}
		err := timeouts.Validate()
		assert.NoError(t, err)
	})
}

func TestWithDefaultHTTPTimeouts(t *testing.T) {
	timeouts := HTTPTimeouts{
		Total: 10 * time.Second,
		Dial:  5 * time.Second,
	}
	w := New(WithDefaultHTTPTimeouts(timeouts))
	defer func() {
		err := w.Close()
		assert.NoError(t, err)
	}()

	assert.True(t, w.hasDefaultHTTPTimeouts)
	assert.Equal(t, timeouts, w.defaultHTTPTimeouts)
}

func TestWithDefaultWSTimeouts(t *testing.T) {
	timeouts := WSTimeouts{
		Handshake: 5 * time.Second,
		Read:      30 * time.Second,
		Write:     10 * time.Second,
	}
	w := New(WithDefaultWSTimeouts(timeouts))
	defer func() {
		err := w.Close()
		assert.NoError(t, err)
	}()

	assert.True(t, w.hasDefaultWSTimeouts)
	assert.Equal(t, timeouts, w.defaultWSTimeouts)
}
