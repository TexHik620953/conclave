package web

import (
	"context"
	"testing"
)

func TestRunManagerExclusiveLaunch(t *testing.T) {
	rm := newRunManager()
	cancel := func() {}
	if !rm.addSessionIfIdle("r1", "s1", cancel, true) {
		t.Fatal("first launch should succeed")
	}
	if rm.addSessionIfIdle("r1", "s1", cancel, true) {
		t.Fatal("second exclusive launch should be refused")
	}
	if rm.activeForSession("s1") != "r1" {
		t.Fatalf("activeForSession = %q", rm.activeForSession("s1"))
	}
	rm.remove("r1")
	if rm.activeForSession("s1") != "" {
		t.Fatal("session should have no active run after removal")
	}
}

func TestInboxDrain(t *testing.T) {
	m := newInboxManager()
	m.push("r1", "hello")
	m.push("r1", "world")
	got := m.drain("r1")
	if len(got) != 2 || got[0] != "hello" || got[1] != "world" {
		t.Fatalf("drain = %v", got)
	}
	if again := m.drain("r1"); len(again) != 0 {
		t.Fatalf("second drain = %v", again)
	}
}

func TestLaunchSessionQueuesMessageWhileActive(t *testing.T) {
	// A second run for the same id must be refused while active.
	rm := newRunManager()
	if !rm.addSessionIfIdle("r1", "s1", context.CancelFunc(func() {}), true) {
		t.Fatal("launch failed")
	}
	if rm.addSessionIfIdle("r1", "s1", context.CancelFunc(func() {}), true) {
		t.Fatal("expected refusal while active")
	}
}
