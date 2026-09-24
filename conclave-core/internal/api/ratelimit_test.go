package api

import "testing"

func TestRateLimiterBurstThenDeny(t *testing.T) {
	rl := newRateLimiter(1, 2)
	if !rl.allow("t1") || !rl.allow("t1") {
		t.Fatal("first two requests should pass")
	}
	if rl.allow("t1") {
		t.Fatal("third immediate request should be limited")
	}
	if !rl.allow("t2") {
		t.Fatal("a different key has its own bucket")
	}
}

func TestRateLimiterDisabled(t *testing.T) {
	if newRateLimiter(0, 0) != nil {
		t.Fatal("zero rate should disable limiting")
	}
}
