package main

import (
	"context"
	"os"

	"github.com/spf13/cobra"

	"github.com/texhik/conclave/internal/config"
	"github.com/texhik/conclave/internal/event"
	"github.com/texhik/conclave/internal/orchestrator"
	"github.com/texhik/conclave/internal/role"
	"github.com/texhik/conclave/internal/store"
	"github.com/texhik/conclave/internal/tool"
	"github.com/texhik/conclave/internal/web"
)

func newServeCmd() *cobra.Command {
	var addr, token string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve the web UI, REST API and WebSocket",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, err := loadApp(ctx, true)
			if err != nil {
				return err
			}
			defer a.Close()
			for _, e := range a.mcpErrors() {
				cmd.PrintErrln("warning:", e)
			}
			srv := web.New(ctx, webDeps{a: a}, web.Options{Token: token})
			srv.Broker().Emit = a.engine.EmitEvent
			a.engine.Asker = srv.Broker()
			a.engine.Inbox = srv.Inbox
			return srv.ListenAndServe(ctx, addr)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:8080", "listen address")
	cmd.Flags().StringVar(&token, "token", "", "optional bearer token for /api")
	return cmd
}

type webDeps struct{ a *app }

func (d webDeps) Config() *config.Config { return d.a.cfg }

func (d webDeps) ListRuns(limit int) ([]store.Run, error) { return d.a.store.ListRuns(limit) }

func (d webDeps) GetRun(id string) (*store.Run, error) { return d.a.store.GetRun(id) }

func (d webDeps) ListNodes(id string) ([]store.NodeRecord, error) { return d.a.store.ListNodes(id) }

func (d webDeps) ListArtifacts(id string) ([]store.Artifact, error) {
	return d.a.store.ListArtifacts(id)
}

func (d webDeps) ListEvents(id string, limit int) ([]store.EventRecord, error) {
	return d.a.store.ListEvents(id, limit)
}

func (d webDeps) ListRecentEvents(id string, limit int) ([]store.EventRecord, error) {
	return d.a.store.ListRecentEvents(id, limit)
}

func (d webDeps) ArtifactContent(runID, name string) (string, error) {
	arts, err := d.a.store.ListArtifacts(runID)
	if err != nil {
		return "", err
	}
	for _, a := range arts {
		if a.Name == name {
			data, err := os.ReadFile(a.Path)
			if err != nil {
				return "", err
			}
			return string(data), nil
		}
	}
	return "", os.ErrNotExist
}

func (d webDeps) Bus() *event.Bus { return d.a.bus }

func (d webDeps) CreateSession(s store.Session) error { return d.a.store.CreateSession(s) }
func (d webDeps) GetSession(id string) (*store.Session, error) {
	return d.a.store.GetSession(id)
}
func (d webDeps) ListSessions(limit int) ([]store.Session, error) {
	return d.a.store.ListSessions(limit)
}
func (d webDeps) DeleteSession(id string) error { return d.a.store.DeleteSession(id) }
func (d webDeps) TouchSessionTitle(id, title string) error {
	return d.a.store.TouchSession(id, title)
}
func (d webDeps) ListMessages(sessionID string, limit int) ([]store.Message, error) {
	return d.a.store.ListMessages(sessionID, limit)
}
func (d webDeps) ListRunsBySession(sessionID string, limit int) ([]store.Run, error) {
	return d.a.store.ListRunsBySession(sessionID, limit)
}
func (d webDeps) AppendMessage(m store.Message) (int64, error) {
	return d.a.store.AppendMessage(m)
}
func (d webDeps) EmitEvent(ev event.Event) { d.a.engine.EmitEvent(ev) }

func (d webDeps) SetRunStatus(id, status string) error { return d.a.store.SetStatus(id, status) }

func (d webDeps) RevertFrom(runID, nodeID string) error { return d.a.store.RevertFrom(runID, nodeID) }

func (d webDeps) RunPipeline(ctx context.Context, opts orchestrator.RunOptions) (*orchestrator.RunResult, error) {
	return d.a.engine.Run(ctx, opts)
}

func (d webDeps) AskRole(ctx context.Context, runID, roleID, prompt, contextText, workspace string) error {
	rt, err := d.a.buildRoleRuntime(ctx, roleID, workspace)
	if err != nil {
		return err
	}
	rt.RunID = runID
	rt.NodeID = "ask"
	ctx = tool.WithRunID(ctx, runID)
	d.a.bus.Publish(event.Event{Type: event.RunStarted, RunID: runID, Role: roleID, Model: rt.Model, Message: "ask " + roleID})
	_, err = rt.Run(ctx, role.Input{Prompt: prompt, Context: contextText})
	status := "completed"
	if err != nil {
		status = "failed"
		if ctx.Err() != nil {
			status = "aborted"
		}
	}
	d.a.bus.Publish(event.Event{Type: event.RunFinished, RunID: runID, Role: roleID, Model: rt.Model, Message: status})
	return err
}
