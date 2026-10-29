package domain

import "testing"

func TestUsageLedgerAggregatesModelAndAgent(t *testing.T) {
	var l UsageLedger
	l.Add(UsageRecord{Provider: "p", Model: "m", AgentID: "a", InputTokens: 10, OutputTokens: 2, Requests: 1, LastContext: 10, ContextWindow: 100})
	l.Add(UsageRecord{Provider: "p", Model: "m", AgentID: "b", InputTokens: 20, OutputTokens: 3, Requests: 1, LastContext: 20, ContextWindow: 100})
	models := l.Models()
	if len(models) != 1 {
		t.Fatalf("models = %d", len(models))
	}
	m := models[0]
	if m.InputTokens != 30 || m.OutputTokens != 5 || m.Requests != 2 || m.LastContext != 20 {
		t.Fatalf("model aggregate: %#v", m)
	}
	if len(l.Agents()) != 2 {
		t.Fatalf("agents = %d", len(l.Agents()))
	}
}
