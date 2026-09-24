package session

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/events"
	"github.com/texhik/conclave/conclave-core/internal/orchestrator"
	"github.com/texhik/conclave/conclave-core/internal/playbook"
	"github.com/texhik/conclave/conclave-core/internal/scheduler"
	"github.com/texhik/conclave/conclave-core/internal/store"
)

// NewService constructs a Service and wires the supervisor's artifact writer
// and the plan scheduler.
func NewService(st store.Store, log *events.Log, registry playbook.Provider, sup *orchestrator.Supervisor) *Service {
	svc := &Service{Store: st, Log: log, Playbooks: registry, Supervisor: sup}
	if sup != nil {
		if sup.Artifacts == nil {
			sup.Artifacts = &artifactWriter{store: st}
		}
		svc.Scheduler = &scheduler.Scheduler{
			Store: st, Log: log, Executor: svc, Gates: svc, MaxParallel: 4,
		}
	}
	return svc
}

// artifactWriter stores artifact content inline in Ref for P0. Object storage
// replaces this in a later phase.
type artifactWriter struct {
	store store.Store
}

func (w *artifactWriter) Write(ctx context.Context, sessionID, playbookRunID, name, content string) (string, error) {
	a := domain.Artifact{
		ID:            uuid.NewString(),
		SessionID:     sessionID,
		PlaybookRunID: playbookRunID,
		Name:          name,
		Kind:          "markdown",
		Ref:           content,
		CreatedAt:     time.Now().UTC(),
	}
	if err := w.store.CreateArtifact(ctx, a); err != nil {
		return "", err
	}
	return a.ID, nil
}
