package worker

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNewGenerator(t *testing.T) {
	_, err := NewGenerator(-1)
	assert.Error(t, err)
	_, err = NewGenerator(0)
	assert.NoError(t, err)
	_, err = NewGenerator(1)
	assert.NoError(t, err)
	_, err = NewGenerator(1023)
	assert.NoError(t, err)
	_, err = NewGenerator(1024)
	assert.Error(t, err)
}

func TestNext(t *testing.T) {
	timeNow = func() time.Time {
		parse, _ := time.Parse(time.RFC3339, "2006-01-02T15:04:05Z")
		return parse
	}
	ts := timeNow().UnixMilli()
	expectedID := (ts << 22) | (1 << 12)
	g, _ := NewGenerator(1)
	id, drift := g.Next()
	assert.Equal(t, int64(0), drift)
	assert.Equal(t, expectedID, id)

	expectedID++
	id, drift = g.Next()
	assert.Equal(t, int64(0), drift)
	assert.Equal(t, expectedID, id)

	// clock drift
	timeNow = func() time.Time {
		parse, _ := time.Parse(time.RFC3339, "2006-01-02T15:04:04Z")
		return parse
	}
	_, drift = g.Next()
	assert.Equal(t, int64(1000), drift)
}
