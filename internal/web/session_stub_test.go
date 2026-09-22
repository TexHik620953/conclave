package web

import (
	"github.com/texhik/conclave/internal/event"
	"github.com/texhik/conclave/internal/store"
)

// Session/message stubs for the test Deps implementations.

func (f *fakeDeps) CreateSession(store.Session) error                 { return nil }
func (f *fakeDeps) GetSession(string) (*store.Session, error)         { return &store.Session{}, nil }
func (f *fakeDeps) ListSessions(int) ([]store.Session, error)         { return nil, nil }
func (f *fakeDeps) DeleteSession(string) error                        { return nil }
func (f *fakeDeps) TouchSessionTitle(string, string) error            { return nil }
func (f *fakeDeps) ListMessages(string, int) ([]store.Message, error) { return nil, nil }
func (f *fakeDeps) ListRunsBySession(string, int) ([]store.Run, error) {
	return nil, nil
}
func (f *fakeDeps) AppendMessage(store.Message) (int64, error) { return 0, nil }
func (f *fakeDeps) EmitEvent(ev event.Event)                   { f.bus.Publish(ev) }

func (d *engineDeps) CreateSession(store.Session) error         { return nil }
func (d *engineDeps) GetSession(string) (*store.Session, error) { return &store.Session{}, nil }
func (d *engineDeps) ListSessions(int) ([]store.Session, error) { return nil, nil }
func (d *engineDeps) DeleteSession(string) error                { return nil }
func (d *engineDeps) TouchSessionTitle(string, string) error    { return nil }
func (d *engineDeps) ListMessages(string, int) ([]store.Message, error) {
	return nil, nil
}
func (d *engineDeps) ListRunsBySession(string, int) ([]store.Run, error) {
	return nil, nil
}
func (d *engineDeps) AppendMessage(store.Message) (int64, error) { return 0, nil }
func (d *engineDeps) EmitEvent(ev event.Event)                   { d.bus.Publish(ev) }
