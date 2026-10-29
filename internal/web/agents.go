package web

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/texhik/conclave/internal/domain"
	"github.com/texhik/conclave/internal/tracker"
)

// AgentInfo is the live view of one running model agent. It is updated from
// the workspace event stream and disappears when the agent finishes.
type AgentInfo struct {
	ID              string    `json:"id"`
	Kind            string    `json:"kind"`
	WorkspaceID     string    `json:"workspace_id"`
	SessionID       string    `json:"session_id,omitempty"`
	TaskID          string    `json:"task_id,omitempty"`
	TaskTitle       string    `json:"task_title,omitempty"`
	WorkerID        string    `json:"worker_id,omitempty"`
	Provider        string    `json:"provider,omitempty"`
	Model           string    `json:"model,omitempty"`
	Phase           string    `json:"phase"`
	CurrentTool     string    `json:"current_tool,omitempty"`
	StartedAt       time.Time `json:"started_at"`
	LastSeen        time.Time `json:"last_seen"`
	InputTokens     int       `json:"input_tokens"`
	OutputTokens    int       `json:"output_tokens"`
	CachedTokens    int       `json:"cached_tokens"`
	ReasoningTokens int       `json:"reasoning_tokens"`
	Requests        int       `json:"requests"`
	LastContext     int       `json:"last_context"`
	ContextWindow   int       `json:"context_window"`
}

func agentKind(id string) string {
	switch {
	case id == "master":
		return "master"
	case id == "master-review":
		return "reviewer"
	case strings.HasPrefix(id, "board-"):
		return "worker"
	default:
		return "agent"
	}
}

func (s *Server) newAgentInfo(ws *workspace, ev domain.Event, agentID, phase string) *AgentInfo {
	info := &AgentInfo{
		ID: agentID, Kind: agentKind(agentID), WorkspaceID: ws.ID,
		SessionID: ev.SessionID, TaskID: ev.TaskID,
		Phase: phase, StartedAt: ev.Time, LastSeen: ev.Time,
	}
	if info.StartedAt.IsZero() {
		info.StartedAt = time.Now()
	}
	if payload, ok := ev.Payload.(map[string]any); ok {
		if model, ok := payload["model"].(string); ok {
			info.Model = model
		}
	}
	if info.TaskID != "" {
		if task, err := ws.App.Tracker().Get(context.Background(), info.TaskID); err == nil && task != nil {
			info.TaskTitle = task.Title
			info.WorkerID = task.Assignee
		}
	}
	if info.Kind == "worker" && info.WorkerID == "" {
		info.WorkerID = "worker"
	}
	return info
}

func (s *Server) trackAgent(ws *workspace, ev domain.Event) {
	agentID := strings.TrimSpace(ev.AgentID)
	if agentID == "" {
		return
	}
	ws.agentMu.Lock()
	defer ws.agentMu.Unlock()
	if ws.agents == nil {
		ws.agents = map[string]*AgentInfo{}
	}
	info := ws.agents[agentID]
	if info == nil && ev.Type != "agent.finished" {
		// The queue may have started before the server subscribed to the
		// workspace bus (for example during startup recovery). Recreate the
		// live entry from the first event we do see.
		info = s.newAgentInfo(ws, ev, agentID, "running")
		ws.agents[agentID] = info
	}
	switch ev.Type {
	case "agent.started":
		info = s.newAgentInfo(ws, ev, agentID, "starting")
		ws.agents[agentID] = info
	case "agent.finished", "agent.error", "agent.cancelled":
		if ev.Type == "agent.finished" {
			delete(ws.agents, agentID)
			return
		}
		if info := ws.agents[agentID]; info != nil {
			info.Phase = strings.TrimPrefix(ev.Type, "agent.")
			info.LastSeen = ev.Time
		}
	case "assistant.started", "assistant.delta", "assistant.reasoning", "assistant.completed":
		if info := ws.agents[agentID]; info != nil {
			info.LastSeen = ev.Time
			switch ev.Type {
			case "assistant.started":
				info.Phase = "thinking"
				info.CurrentTool = ""
			case "assistant.delta", "assistant.reasoning":
				info.Phase = "streaming"
			case "assistant.completed":
				info.Phase = "thinking"
			}
		}
	case "tool.started", "tool.running", "tool.completed", "tool.failed":
		if info := ws.agents[agentID]; info != nil {
			info.LastSeen = ev.Time
			info.Phase = "tool"
			if payload, ok := ev.Payload.(map[string]any); ok {
				if name, ok := payload["name"].(string); ok && name != "" {
					info.CurrentTool = name
				}
				if call, ok := payload["call"].(domain.ToolCall); ok {
					info.CurrentTool = call.Name
				}
			}
			if ev.Type == "tool.completed" || ev.Type == "tool.failed" {
				info.CurrentTool = ""
			}
		}
	case "usage.updated":
		if info := ws.agents[agentID]; info != nil {
			info.LastSeen = ev.Time
			if rec, ok := ev.Payload.(domain.UsageRecord); ok {
				info.Provider = rec.Provider
				if rec.Model != "" {
					info.Model = rec.Provider + "/" + rec.Model
				}
				info.InputTokens += rec.InputTokens
				info.OutputTokens += rec.OutputTokens
				info.CachedTokens += rec.CachedTokens
				info.ReasoningTokens += rec.ReasoningTokens
				info.Requests += rec.Requests
				if rec.LastContext > 0 {
					info.LastContext = rec.LastContext
				}
				if rec.ContextWindow > 0 {
					info.ContextWindow = rec.ContextWindow
				}
			}
		}
	}
}

func (s *Server) agentsList(w http.ResponseWriter, r *http.Request) {
	ws := s.get(r.URL.Query().Get("id"))
	if ws == nil {
		httpError(w, fmt.Errorf("workspace not found"), http.StatusNotFound)
		return
	}
	ws.agentMu.Lock()
	byID := make(map[string]AgentInfo, len(ws.agents))
	for id, info := range ws.agents {
		byID[id] = *info
	}
	ws.agentMu.Unlock()

	// Event tracking can miss the very first event if recovery started the
	// queue before the server subscribed. Board attempts are the authoritative
	// fallback for currently running workers.
	if tasks, err := ws.App.Tracker().List(context.Background(), tracker.Filter{Status: []tracker.TaskStatus{tracker.StatusClaimed, tracker.StatusInProgress}, Limit: 100}); err == nil {
		for _, task := range tasks {
			found := false
			for _, info := range byID {
				if info.TaskID == task.ID {
					found = true
					break
				}
			}
			if found {
				continue
			}
			attempts, _ := ws.App.Tracker().Attempts(context.Background(), task.ID)
			info := AgentInfo{
				ID: "board-" + strings.ReplaceAll(task.ID, "-", ""), Kind: "worker",
				WorkspaceID: ws.ID, TaskID: task.ID, TaskTitle: task.Title,
				WorkerID: task.Assignee, Phase: "running",
				StartedAt: task.UpdatedAt, LastSeen: time.Now(),
			}
			if len(attempts) > 0 {
				attempt := attempts[len(attempts)-1]
				if attempt.AgentID != "" {
					info.ID = attempt.AgentID
				}
				info.WorkerID = attempt.Worker
				info.Model = attempt.Model
				if attempt.StartedAt.After(info.StartedAt) {
					info.StartedAt = attempt.StartedAt
				}
			}
			if provider, model, ok := strings.Cut(info.Model, "/"); ok {
				info.Provider, info.Model = provider, model
			}
			if _, exists := byID[info.ID]; !exists {
				byID[info.ID] = info
			}
		}
	}
	out := make([]AgentInfo, 0, len(byID))
	for _, info := range byID {
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	writeJSON(w, http.StatusOK, map[string]any{"agents": out})
}
