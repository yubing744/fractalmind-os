// Package localruntime delivers messages and wakeups to agents running on this
// machine. Every runtime implements one Adapter contract: Deliver a request,
// report the agent's Presence, and declare Capabilities so callers choose
// behavior from declared facts instead of runtime names.
//
// Runtimes: Claude Desktop (a durable per-agent inbox consumed by a relay in
// the agent's session), Codex App (the native queue of a pinned thread),
// DeepSeek Harness Desktop (its active composer through an explicitly enabled
// local CDP endpoint) and agent-manager (tmux agents). The inbox layout is the
// versioned on-disk protocol in docs/inbox-protocol.md, so any process can
// produce or consume it.
//
// This package is an application-layer library. FractalBot embeds it; it does
// not depend on FractalBot's configuration.
package localruntime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// Status is a delivery outcome.
type Status string

const (
	// Delivered: the agent's session received the request.
	Delivered Status = "delivered"
	// Queued: the request is durable and the agent will take it, but has not
	// yet. An inbox write or a Codex queue entry.
	Queued Status = "queued"
	// Completed: a completion-aware request ran to its terminal outcome.
	Completed Status = "completed"
	// Failed: nothing durable was created, or a completion-aware request ended
	// in failure. Err says which.
	Failed Status = "failed"
)

// Presence is whether an agent can take a delivery now.
type Presence string

const (
	Online  Presence = "online"
	Offline Presence = "offline" // deliveries wait in a durable queue
	Unknown Presence = "unknown"
)

// Capabilities are what a runtime can do. Callers decide from these, never
// from the runtime's name.
type Capabilities struct {
	// DurableQueue: a delivery survives the agent's session being closed.
	DurableQueue bool `json:"durable_queue"`
	// WakesIdle: a delivery starts work in an idle session without a person.
	WakesIdle bool `json:"wakes_idle"`
	// Start: the runtime can start a stopped agent.
	Start bool `json:"start"`
	// HardStop: the runtime can stop an agent's work at process level.
	HardStop bool `json:"hard_stop"`
	// CompletionAware: Mode HeartbeatRun waits for the terminal outcome.
	CompletionAware bool `json:"completion_aware"`
	// PresenceKnown: Presence can answer Online or Offline.
	PresenceKnown bool `json:"presence_known"`
}

// Target is one agent inside a runtime.
type Target struct {
	// Agent is the agent's persistent name; it names its inbox.
	Agent string
	// Workspace is the agent's working directory.
	Workspace string
	// Session is the receiver inside the runtime: the agent-manager agent, the
	// Codex thread ID, or the DeepSeek Harness session ID. Claude Desktop agents
	// have none.
	Session string
	// Inbox is the agent's inbox directory, for runtimes that use one.
	Inbox string
}

// Mode selects how a request runs.
type Mode string

const (
	// Assign hands over the prompt; the default.
	Assign Mode = ""
	// HeartbeatRun runs the runtime's own heartbeat lifecycle and waits for
	// its outcome. Only runtimes with CompletionAware support it.
	HeartbeatRun Mode = "heartbeatRun"
)

// Request is one message or wakeup.
type Request struct {
	Envelope Envelope
	// Prompt is the complete text the agent receives.
	Prompt string
	Mode   Mode
	// Timeout bounds the delivery; zero uses the runtime's default.
	Timeout time.Duration
}

// Result is a delivery outcome.
type Result struct {
	Status     Status
	EnvelopeID string
	// Location is where a queued request waits: an inbox file or a thread.
	Location string
	Err      error
}

// Adapter is one runtime.
type Adapter interface {
	Name() string
	Capabilities() Capabilities
	Deliver(ctx context.Context, target Target, req Request) Result
	Presence(ctx context.Context, target Target) Presence
}

// Starter is implemented by runtimes with Capabilities.Start.
type Starter interface {
	Start(ctx context.Context, target Target) error
}

// NewEnvelopeID returns a random envelope ID.
func NewEnvelopeID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func failed(req Request, err error) Result {
	return Result{Status: Failed, EnvelopeID: req.Envelope.ID, Err: err}
}
