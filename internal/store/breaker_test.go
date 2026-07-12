package store

import (
	"testing"
	"time"
)

func testBreaker(threshold int, cooldown time.Duration) (*Breaker, *fakeBreakerClock) {
	b := NewBreaker(threshold, cooldown)
	c := &fakeBreakerClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	b.now = c.Now
	return b, c
}

type fakeBreakerClock struct{ t time.Time }

func (c *fakeBreakerClock) Now() time.Time          { return c.t }
func (c *fakeBreakerClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

func TestBreakerOpensAtThreshold(t *testing.T) {
	b, _ := testBreaker(3, time.Second)

	b.failure()
	b.failure()
	if !b.allow() || b.Degraded() {
		t.Fatal("breaker open before threshold")
	}
	b.failure()
	if b.allow() || !b.Degraded() {
		t.Fatal("breaker not open at threshold")
	}
}

func TestBreakerSuccessResetsCount(t *testing.T) {
	b, _ := testBreaker(3, time.Second)

	b.failure()
	b.failure()
	b.success() // consecutive means consecutive
	b.failure()
	b.failure()
	if b.Degraded() {
		t.Fatal("breaker opened despite interleaved success")
	}
}

func TestBreakerProbesAfterCooldown(t *testing.T) {
	b, clock := testBreaker(3, time.Second)
	for i := 0; i < 3; i++ {
		b.failure()
	}

	if b.allow() {
		t.Fatal("allowed during cooldown")
	}
	clock.Advance(999 * time.Millisecond)
	if b.allow() {
		t.Fatal("allowed before cooldown elapsed")
	}

	clock.Advance(time.Millisecond)
	if !b.allow() {
		t.Fatal("probe not allowed after cooldown")
	}
	// only one probe at a time
	if b.allow() {
		t.Fatal("second probe allowed while first in flight")
	}

	// probe failure buys another cooldown
	b.failure()
	if b.allow() {
		t.Fatal("allowed right after failed probe")
	}
	clock.Advance(time.Second)
	if !b.allow() {
		t.Fatal("second probe not allowed")
	}
	// probe success closes
	b.success()
	if b.Degraded() || !b.allow() {
		t.Fatal("breaker still open after successful probe")
	}
}

func TestBreakerOnChangeFiresPerTransition(t *testing.T) {
	b, clock := testBreaker(3, time.Second)
	var calls []bool
	b.OnChange(func(degraded bool) { calls = append(calls, degraded) })

	for i := 0; i < 5; i++ {
		b.failure() // extra failures must not re-fire
	}
	clock.Advance(time.Second)
	b.allow()
	b.failure() // failed probe: still degraded, no re-fire
	clock.Advance(time.Second)
	b.allow()
	b.success() // recovered

	want := []bool{true, false}
	if len(calls) != len(want) || calls[0] != want[0] || calls[1] != want[1] {
		t.Errorf("onChange calls = %v, want %v", calls, want)
	}
}

func TestNilBreakerIsNoop(t *testing.T) {
	var b *Breaker
	if !b.allow() || b.Degraded() {
		t.Fatal("nil breaker should always allow and never report degraded")
	}
	b.success() // must not panic
	b.failure()
}
