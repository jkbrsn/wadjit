package wadjit

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewWadjit(t *testing.T) {
	w := New()
	assert.NotNil(t, w)
	assert.NotNil(t, w.taskManager)
}
