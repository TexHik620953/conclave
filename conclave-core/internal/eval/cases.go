package eval

import (
	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/orchestrator"
	"github.com/texhik/conclave/conclave-core/internal/session"
)

// GoldenCases returns the built-in golden task set.
func GoldenCases() []Case {
	return []Case{
		{
			Name:       "ba-online-shop",
			PlaybookID: "ba",
			Idea:       "build an online shop",
			Decisions: []orchestrator.Decision{
				{Delegate: &orchestrator.Delegation{Role: "senior", Task: "collect requirements"}},
			},
			Finish:         "# Requirements\n- users can browse and buy",
			RoleOutput:     func(in orchestrator.RoleInput) string { return "goals: sell online; users: shoppers" },
			ExpectArtifact: "requirements.md",
			ExpectContains: []string{"Requirements", "browse and buy"},
			ExpectEvents: []string{
				domain.EventPlaybookStarted,
				domain.EventTaskFinished,
				domain.EventArtifactCreated,
				domain.EventPlaybookFinished,
			},
		},
		{
			Name:       "general-task",
			PlaybookID: "general",
			Idea:       "summarize the codebase",
			Decisions: []orchestrator.Decision{
				{Delegate: &orchestrator.Delegation{Role: "junior", Task: "read the docs"}},
			},
			Finish:         "summary written",
			ExpectArtifact: "result.md",
			ExpectEvents:   []string{domain.EventPlaybookFinished},
		},
		{
			Name: "plan-gate-waits",
			Plan: []session.PlanNodeSpec{
				{ID: "a", PlaybookID: "ba"},
				{ID: "g", Kind: "gate", Title: "Approve?"},
				{ID: "b", PlaybookID: "general"},
			},
			Edges: []session.PlanEdgeSpec{
				{From: "a", To: "g"},
				{From: "a", To: "b"},
			},
			Finish:           "done",
			ExpectPlanStatus: "waiting",
			ExpectQuestions:  1,
			ExpectEvents:     []string{domain.EventQuestionAsked},
		},
		{
			Name: "plan-gate-auto-accept",
			Plan: []session.PlanNodeSpec{
				{ID: "a", PlaybookID: "ba"},
				{ID: "g", Kind: "gate", Title: "Approve?"},
			},
			Edges:            []session.PlanEdgeSpec{{From: "a", To: "g"}},
			Finish:           "done",
			AutoAccept:       true,
			ExpectPlanStatus: "done",
			ExpectQuestions:  0,
		},
		{
			Name:       "critic-reopens-and-escalates",
			PlaybookID: "ba",
			Idea:       "write requirements",
			Brain: func(in orchestrator.DecisionInput) orchestrator.Decision {
				if len(in.History) == 0 {
					return orchestrator.Decision{Delegate: &orchestrator.Delegation{Role: "auto", Task: "draft"}}
				}
				if in.History[len(in.History)-1].Role == "critic" {
					return orchestrator.Decision{Delegate: &orchestrator.Delegation{Role: "auto", Task: "fix"}}
				}
				return orchestrator.Decision{Finish: "final"}
			},
			Critic: []orchestrator.CriticVerdict{
				{Pass: false, Feedback: "missing acceptance criteria", SuggestedTier: "senior"},
				{Pass: true},
			},
			RoleOutput:     func(in orchestrator.RoleInput) string { return "work by " + in.Role.Tier },
			ExpectArtifact: "requirements.md",
			ExpectEvents:   []string{domain.EventCriticVerdict, domain.EventPlaybookFinished},
		},
	}
}
