package worker

import (
	"fmt"
	"sync"
	"time"
)

var timeNow = time.Now

const (
	workerIDBits       = int64(10)
	sequenceIDBits     = int64(12)
	timestampShiftBits = workerIDBits + sequenceIDBits

	sequenceIDMask = (1 << sequenceIDBits) - 1
)

// Generator generates snowflake id.
// Warning: Generator struct contains a mutex. Thus, it is not safe to copy Generator.
type Generator struct {
	previousTimestamp int64
	sequenceID        int64
	mu                sync.Mutex

	serveIDShifted int64
}

// NewGenerator returns a generator if the worker id is less than 1024.
func NewGenerator(workerID int) (*Generator, error) {
	if workerID < 0 || workerID > ((1<<workerIDBits)-1) {
		return nil, fmt.Errorf("worker id %d too large to fit in %d bits", workerID, workerIDBits)
	}
	return &Generator{serveIDShifted: int64(workerID) << sequenceIDBits}, nil
}

// Next returns snowflake ID and clock drift. If there is clock drift, the snowflake ID is 0.
//
// The snowflake id is a 64-bits long integer.
// bit 1 (most significant bit): set to 0, indicate this is a positive number.
// bit 2-42: Unix epoch timestamp in millis.
// bit 43-52: 10 bits to represent the worker ID. The worker id uniquely identify a process that generates Snowflake ID.
// bit 53-64: 12 bits. Sequence numbers. Each millisecond, there are 4096 IDs.
func (g *Generator) Next() (int64, int64) {
	g.mu.Lock()
	defer g.mu.Unlock()

	timestamp := timeNow().UTC().UnixMilli()
	drift := g.previousTimestamp - timestamp
	switch {
	// New millisecond, reset the sequence ID to 0.
	case drift < 0:
		g.sequenceID = 0
	// Same millisecond, try increasing sequence ID.
	case drift == 0:
		g.sequenceID = (g.sequenceID + 1) & sequenceIDMask
		// exhausted all sequence IDs, block till next millisecond.
		if g.sequenceID == 0 {
			timestamp = blockPastMillis(timestamp)
			g.sequenceID = 0
		}
	default:
		return 0, drift
	}
	g.previousTimestamp = timestamp
	return (timestamp << timestampShiftBits) | g.serveIDShifted | g.sequenceID, 0
}

// LastUsedTimestamp returns the last timestamp used to generate snowflake ID.
func (g *Generator) LastUsedTimestamp() int64 {
	return g.previousTimestamp
}

// SetLastUsedTimestamp sets the last timestamp used to generate snowflake ID.
func (g *Generator) SetLastUsedTimestamp(t int64) {
	g.previousTimestamp = t
}

// blockPastMillis blocks till timestamp is larger than t. Returns the timestamp.
func blockPastMillis(t int64) int64 {
	for {
		now := timeNow().UTC().UnixMilli()
		if now > t {
			return now
		}
		if t-now > 5 {
			time.Sleep(5 * time.Millisecond)
		}
	}
}
