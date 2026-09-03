package event

import (
	"sync"
	"time"

	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

type Clock struct {
	mu   sync.Mutex
	node string
	last protocol.HLC
	now  func() time.Time
}

func NewClock(node string) *Clock {
	return &Clock{node: node, now: time.Now}
}

func (c *Clock) Now() protocol.HLC {
	c.mu.Lock()
	defer c.mu.Unlock()
	wall := c.now().UnixMilli()
	if wall > c.last.WallTimeMS {
		c.last = protocol.HLC{WallTimeMS: wall, Node: c.node}
	} else {
		c.last.Counter++
	}
	return c.last
}

func (c *Clock) Observe(remote protocol.HLC) protocol.HLC {
	c.mu.Lock()
	defer c.mu.Unlock()
	wall := c.now().UnixMilli()
	maxWall := max(wall, c.last.WallTimeMS, remote.WallTimeMS)
	var counter uint32
	switch {
	case maxWall == c.last.WallTimeMS && maxWall == remote.WallTimeMS:
		counter = max(c.last.Counter, remote.Counter) + 1
	case maxWall == c.last.WallTimeMS:
		counter = c.last.Counter + 1
	case maxWall == remote.WallTimeMS:
		counter = remote.Counter + 1
	}
	c.last = protocol.HLC{WallTimeMS: maxWall, Counter: counter, Node: c.node}
	return c.last
}

func Compare(a, b protocol.HLC) int {
	if a.WallTimeMS != b.WallTimeMS {
		if a.WallTimeMS < b.WallTimeMS {
			return -1
		}
		return 1
	}
	if a.Counter != b.Counter {
		if a.Counter < b.Counter {
			return -1
		}
		return 1
	}
	if a.Node < b.Node {
		return -1
	}
	if a.Node > b.Node {
		return 1
	}
	return 0
}
