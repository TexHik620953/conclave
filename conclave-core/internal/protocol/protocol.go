// Package protocol defines the typed frames exchanged between conclave-core
// and a local agent, plus a JSON codec and a length-prefixed stream framing.
//
// The protocol is transport-agnostic: over WebSocket each frame is one text
// message; over a byte stream use WriteFrame/ReadFrame.
package protocol

import "encoding/json"

// Version is the current protocol version.
const Version = 1

// Frame types.
const (
	// Control.
	TypeHello   = "hello"   // agent -> core, capability handshake
	TypeWelcome = "welcome" // core -> agent, accepted + negotiated version
	TypePing    = "ping"
	TypePong    = "pong"
	TypeError   = "error"
	TypeResume  = "resume" // either direction, carries from_seq

	// Session lifecycle.
	TypeSessionOpen  = "session.open"
	TypeSessionClose = "session.close"

	// Plan and tasks.
	TypePlanUpdate   = "plan.update"
	TypeTaskAssigned = "task.assigned"
	TypeTaskProgress = "task.progress"
	TypeTaskResult   = "task.result"

	// Tools.
	TypeToolCall   = "tool.call"
	TypeToolResult = "tool.result"

	// Events and human input.
	TypeEventAppend = "event.append"
	TypeQuestion    = "question"
	TypeAnswer      = "answer"

	// Data and accounting.
	TypeArtifact = "artifact"
	TypeUsage    = "usage"
)

// Frame is one protocol message. Data carries a type-specific JSON payload.
type Frame struct {
	V         int             `json:"v"`
	Type      string          `json:"type"`
	ID        string          `json:"id,omitempty"`
	ReplyTo   string          `json:"reply_to,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	Seq       int64           `json:"seq,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// Capabilities describes what a local agent can do. It is sent in Hello and is
// injected into role context so models can adapt to the environment.
type Capabilities struct {
	OS         string   `json:"os"`
	Arch       string   `json:"arch"`
	Shell      string   `json:"shell"`
	Workspace  string   `json:"workspace,omitempty"`
	Languages  []string `json:"languages,omitempty"`
	BuildTools []string `json:"build_tools,omitempty"`
	Linters    []string `json:"linters,omitempty"`
	Git        bool     `json:"git"`
	MCPServers []string `json:"mcp_servers,omitempty"`
}

// Hello is the agent's opening frame.
type Hello struct {
	Protocol     int          `json:"protocol"`
	AgentVersion string       `json:"agent_version,omitempty"`
	DeviceID     string       `json:"device_id,omitempty"`
	Capabilities Capabilities `json:"capabilities"`
}

// Welcome is the core's acceptance of a Hello.
type Welcome struct {
	Protocol    int    `json:"protocol"`
	SessionID   string `json:"session_id,omitempty"`
	CoreVersion string `json:"core_version,omitempty"`
}

// ResumeRequest asks for events after FromSeq.
type ResumeRequest struct {
	FromSeq int64 `json:"from_seq"`
}

// ToolCall asks the agent to execute a tool.
type ToolCall struct {
	CallID string          `json:"call_id"`
	Name   string          `json:"name"`
	Args   json.RawMessage `json:"args,omitempty"`
	// IdempotencyKey makes retries after a reconnect safe.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

// ToolResult is the outcome of a ToolCall.
type ToolResult struct {
	CallID  string `json:"call_id"`
	Name    string `json:"name"`
	IsError bool   `json:"is_error,omitempty"`
	Content string `json:"content,omitempty"`
}
