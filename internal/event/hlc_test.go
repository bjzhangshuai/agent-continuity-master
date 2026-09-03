package event

import (
	"testing"
	"time"

	"github.com/agent-continuity/agent-continuity-master/pkg/protocol"
)

func TestClockMonotonicWhenWallClockStalls(t *testing.T) {
	c := NewClock("device-a")
	c.now = func() time.Time { return time.UnixMilli(1000) }
	a, b := c.Now(), c.Now()
	if Compare(a, b) >= 0 || b.Counter != 1 {
		t.Fatalf("clock did not advance: a=%+v b=%+v", a, b)
	}
}

func TestObservePreservesCausality(t *testing.T) {
	c := NewClock("device-b")
	c.now = func() time.Time { return time.UnixMilli(1000) }
	got := c.Observe(protocol.HLC{WallTimeMS: 2000, Counter: 7, Node: "device-a"})
	if got.WallTimeMS != 2000 || got.Counter != 8 {
		t.Fatalf("unexpected observed clock: %+v", got)
	}
}
