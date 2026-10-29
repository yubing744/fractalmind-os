// Package router delivers channel messages and scheduled wakeups to agents.
// A message arrives on one channel instance; the router picks the receiving
// agent among those bound to it (an explicit mention, then a pinned chat, then
// the conversation's /use choice, then the channel's default), builds the
// prompt and hands it to the agent's runtime through localruntime.
package router

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fractalmind-labs/fractalmind-os/runtime/fractalbot/internal/agentruntime"
	"github.com/fractalmind-labs/fractalmind-os/runtime/fractalbot/internal/channels"
	"github.com/fractalmind-labs/fractalmind-os/runtime/fractalbot/internal/configv2"
	"github.com/fractalmind-labs/fractalmind-os/runtime/fractalbot/pkg/localruntime"
	"github.com/fractalmind-labs/fractalmind-os/runtime/fractalbot/pkg/protocol"
)

const (
	defaultAckMessage   = "处理中…"
	seenRetention       = 24 * time.Hour
	seenMax             = 4096
	toolsUnavailableMsg = "⚠️ /tool and /tools are not available in gateway mode."
)

// How the receiver of a message was chosen.
const (
	SelectedByMention = "mention"
	SelectedByPinned  = "pinned"
	SelectedByUse     = "use"
	SelectedByDefault = "default"
)

// Outcome is the last delivery to an agent, for status.
type Outcome struct {
	Source     string    `json:"source"` // a channel instance or "schedule:<job>"
	SelectedBy string    `json:"selected_by,omitempty"`
	Status     string    `json:"status"`
	EnvelopeID string    `json:"envelope_id,omitempty"`
	Location   string    `json:"location,omitempty"`
	Error      string    `json:"error,omitempty"`
	At         time.Time `json:"at"`
}

// Router routes messages and wakeups to the configured agents.
type Router struct {
	cfg        *configv2.Config
	adapters   map[string]localruntime.Adapter
	selections *selectionStore
	bodyDir    string

	feishuByApp map[string]string // Feishu app ID -> channel ref
	soleByType  map[string]string // channel type -> its one instance ref

	hookMu      sync.RWMutex
	inboundHook func(agent string)

	seenMu sync.Mutex
	seen   map[string]time.Time

	outcomeMu sync.RWMutex
	outcomes  map[string]Outcome
}

// New builds a router for a validated configuration. Adapters default to the
// configured runtimes; tests replace them.
func New(cfg *configv2.Config, adapters map[string]localruntime.Adapter) *Router {
	if adapters == nil {
		adapters = DefaultAdapters(cfg)
	}
	r := &Router{
		cfg:         cfg,
		adapters:    adapters,
		selections:  newSelectionStore(cfg.Paths().Selections()),
		bodyDir:     filepath.Join(os.TempDir(), "fractalbot", "bodies"),
		feishuByApp: map[string]string{},
		soleByType:  map[string]string{},
		seen:        map[string]time.Time{},
		outcomes:    map[string]Outcome{},
	}
	for name, f := range cfg.Channels.Feishu {
		r.feishuByApp[strings.TrimSpace(f.AppID)] = configv2.ChannelFeishu + "." + name
	}
	for _, inst := range cfg.ChannelList() {
		if inst.Type != configv2.ChannelFeishu {
			r.soleByType[inst.Type] = inst.Ref
		}
	}
	return r
}

// DefaultAdapters are the runtimes as the configuration tunes them.
func DefaultAdapters(cfg *configv2.Config) map[string]localruntime.Adapter {
	return map[string]localruntime.Adapter{
		configv2.RuntimeClaudeDesktop: localruntime.ClaudeDesktop{},
		configv2.RuntimeCodexApp: localruntime.CodexApp{
			Codex:   cfg.Runtimes.CodexApp.Codex,
			Timeout: seconds(cfg.Runtimes.CodexApp.DeliveryTimeoutSeconds),
		},
		configv2.RuntimeDeepSeekHarness: localruntime.DeepSeekHarness{
			CDPEndpoint:    cfg.Runtimes.DeepSeekHarness.CDPEndpoint,
			TargetSelector: cfg.Runtimes.DeepSeekHarness.TargetSelector,
			Timeout:        seconds(cfg.Runtimes.DeepSeekHarness.DeliveryTimeoutSeconds),
		},
		configv2.RuntimeAgentManager: localruntime.AgentManager{
			Script: func(t localruntime.Target) string {
				return cfg.AgentManagerScript(configv2.Agent{Workspace: t.Workspace})
			},
			AssignTimeout: seconds(cfg.Runtimes.AgentManager.AssignTimeoutSeconds),
		},
	}
}

func seconds(n int) time.Duration { return time.Duration(n) * time.Second }

// SetInboundHook is called with the agent after each delivered message, so a
// schedule can restore its default frequency.
func (r *Router) SetInboundHook(hook func(agent string)) {
	r.hookMu.Lock()
	r.inboundHook = hook
	r.hookMu.Unlock()
}

// Target is an agent's runtime target.
func (r *Router) Target(name string) localruntime.Target {
	a := r.cfg.Agents[name]
	return localruntime.Target{Agent: name, Workspace: a.Workspace, Session: a.Session, Inbox: r.cfg.Paths().Inbox(name)}
}

// ChannelRef identifies the channel instance a message arrived on.
func (r *Router) ChannelRef(data map[string]interface{}) (string, bool) {
	kind := value(data, "channel")
	if kind == configv2.ChannelFeishu {
		ref, ok := r.feishuByApp[value(data, "receiver_id")]
		return ref, ok
	}
	ref, ok := r.soleByType[kind]
	return ref, ok
}

// Shared reports whether more than one agent receives a channel instance.
func (r *Router) Shared(ref string) bool { return len(r.cfg.AgentsFor(ref)) > 1 }

// HandleIncoming implements channels.IncomingMessageHandler.
func (r *Router) HandleIncoming(ctx context.Context, msg *protocol.Message) (string, error) {
	return r.HandleIncomingStream(ctx, msg, nil)
}

// HandleIncomingStream implements channels.StreamingHandler: it acknowledges
// the message on its channel before delivering it.
func (r *Router) HandleIncomingStream(ctx context.Context, msg *protocol.Message, sink channels.ReplySink) (string, error) {
	if msg == nil {
		return "", nil
	}
	data, ok := msg.Data.(map[string]interface{})
	if !ok || r.duplicate(messageID(data)) {
		return "", nil
	}
	text := strings.TrimSpace(value(data, "text"))
	// Demail carries agent-to-agent mail, which FractalBot transports but does
	// not route to agents.
	if text == "" || value(data, "channel") == configv2.ChannelDemail {
		return "", nil
	}
	ref, ok := r.ChannelRef(data)
	if !ok {
		return "", fmt.Errorf("message from an unconfigured %s channel", value(data, "channel"))
	}
	if reply, handled := r.command(ref, data, text); handled {
		return reply, nil
	}
	if isToolInvocation(text) {
		return toolsUnavailableMsg, nil
	}
	agent, selectedBy, text, err := r.selectAgent(ref, data, text)
	if err != nil {
		return "❌ " + err.Error(), nil
	}
	data["text"] = text
	if err := channels.WrapMessageBody(msg, r.bodyDir); err != nil {
		log.Printf("body wrap: %v", err)
	}
	ack := r.ackMessage()
	if sink != nil && ack != "" {
		sink.Ack(ack)
	}
	env := inboundEnvelope(agent, data)
	prompt := buildInboundPrompt(r.cfg.Agents[agent].Runtime, ref, agent, selectedBy, r.Shared(ref), env, data)
	res := r.deliver(ctx, agent, localruntime.Request{Envelope: env, Prompt: prompt})
	r.record(agent, Outcome{Source: ref, SelectedBy: selectedBy, Status: string(res.Status), EnvelopeID: res.EnvelopeID, Location: res.Location, Error: errText(res.Err)})
	if res.Status == localruntime.Failed {
		return "", res.Err
	}
	r.hookMu.RLock()
	hook := r.inboundHook
	r.hookMu.RUnlock()
	if hook != nil {
		hook(agent)
	}
	if sink != nil {
		return "", nil
	}
	return ack, nil
}

func (r *Router) ackMessage() string {
	in := r.cfg.Inbound
	if in.AckEnabled != nil && !*in.AckEnabled {
		return ""
	}
	if msg := strings.TrimSpace(in.AckMessage); msg != "" {
		return msg
	}
	return defaultAckMessage
}

// selectAgent picks the receiver and returns the text with any mention
// removed.
func (r *Router) selectAgent(ref string, data map[string]interface{}, text string) (string, string, string, error) {
	bound := r.cfg.AgentsFor(ref)
	isBound := func(name string) bool {
		for _, b := range bound {
			if b == name {
				return true
			}
		}
		return false
	}
	// The channel validated /agent and /to against the bound agents.
	if specified, _ := data["agent_specified"].(bool); specified {
		if name := value(data, "agent"); isBound(name) {
			return name, SelectedByMention, text, nil
		}
		return "", "", "", fmt.Errorf("%q does not receive this channel; choose one of: %s", value(data, "agent"), strings.Join(bound, ", "))
	}
	if strings.HasPrefix(text, "@") {
		head, rest, _ := strings.Cut(text[1:], " ")
		if isBound(head) && strings.TrimSpace(rest) != "" {
			return head, SelectedByMention, strings.TrimSpace(rest), nil
		}
	}
	chat := value(data, "chat_id")
	inst := r.instance(ref)
	if name, ok := inst.Sharing.Chats[chat]; ok && isBound(name) {
		return name, SelectedByPinned, text, nil
	}
	if name := r.selections.get(ref, chat); name != "" && isBound(name) {
		return name, SelectedByUse, text, nil
	}
	if inst.Sharing.Default != "" {
		return inst.Sharing.Default, SelectedByDefault, text, nil
	}
	if len(bound) == 1 {
		return bound[0], SelectedByDefault, text, nil
	}
	return "", "", "", errors.New("no default agent for this channel")
}

func (r *Router) instance(ref string) configv2.ChannelInstance {
	for _, inst := range r.cfg.ChannelList() {
		if inst.Ref == ref {
			return inst
		}
	}
	return configv2.ChannelInstance{}
}

// command handles /use and /who. Other text is not a router command.
func (r *Router) command(ref string, data map[string]interface{}, text string) (string, bool) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", false
	}
	bound := r.cfg.AgentsFor(ref)
	chat := value(data, "chat_id")
	switch strings.ToLower(fields[0]) {
	case "/who":
		agent, selectedBy, _, err := r.selectAgent(ref, map[string]interface{}{"chat_id": chat}, "who")
		if err != nil {
			return "❌ " + err.Error(), true
		}
		return fmt.Sprintf("This conversation goes to %s (%s). Agents on this channel: %s", agent, selectedBy, strings.Join(bound, ", ")), true
	case "/use":
		if len(fields) == 1 {
			if err := r.selections.set(ref, chat, ""); err != nil {
				return "❌ " + err.Error(), true
			}
			agent, _, _, _ := r.selectAgent(ref, map[string]interface{}{"chat_id": chat}, "use")
			return "Back to the default: " + agent, true
		}
		name := fields[1]
		for _, b := range bound {
			if b == name {
				if err := r.selections.set(ref, chat, name); err != nil {
					return "❌ " + err.Error(), true
				}
				return "This conversation now goes to " + name, true
			}
		}
		return fmt.Sprintf("❌ %q does not receive this channel; choose one of: %s", name, strings.Join(bound, ", ")), true
	}
	return "", false
}

// DispatchRuntime implements agentruntime.Dispatcher for schedules.
func (r *Router) DispatchRuntime(ctx context.Context, req agentruntime.DispatchRequest) agentruntime.DispatchResult {
	res := agentruntime.DispatchResult{Runtime: req.Runtime, Agent: req.Agent}
	agent, ok := r.cfg.Agents[req.Agent]
	if !ok {
		res.Status, res.Error = "error", fmt.Sprintf("agent %q is not configured", req.Agent)
		return res
	}
	if req.PromptFile != "" {
		data, err := os.ReadFile(req.PromptFile)
		if err != nil {
			res.Status, res.Error = "error", fmt.Sprintf("read prompt file: %v", err)
			return res
		}
		req.Text = string(data)
	}
	req.Runtime = agent.Runtime
	res.Runtime = agent.Runtime
	adapter := r.adapters[agent.Runtime]
	if req.StartIfMissing {
		if starter, ok := adapter.(localruntime.Starter); ok && adapter.Presence(ctx, r.Target(req.Agent)) == localruntime.Offline {
			if err := starter.Start(ctx, r.Target(req.Agent)); err != nil {
				res.Status, res.Error = "error", err.Error()
				return res
			}
		}
	}
	env := scheduleEnvelope(req)
	out := r.deliver(ctx, req.Agent, localruntime.Request{
		Envelope: env,
		Prompt:   buildSchedulePrompt(req),
		Mode:     localruntime.Mode(req.DispatchMode),
		Timeout:  req.Timeout,
	})
	r.record(req.Agent, Outcome{Source: "schedule:" + req.JobID, Status: string(out.Status), EnvelopeID: out.EnvelopeID, Location: out.Location, Error: errText(out.Err)})
	res.EnvelopeID, res.InboxPath = out.EnvelopeID, out.Location
	switch out.Status {
	case localruntime.Queued:
		res.Status = "queued"
	case localruntime.Delivered:
		res.Status = "assigned"
	case localruntime.Completed:
		res.Status = "heartbeat_terminal"
	default:
		res.Status = "error"
		// A completion-aware run that ended in failure must not be retried.
		if req.DispatchMode == string(localruntime.HeartbeatRun) {
			res.Status = "heartbeat_failed"
		}
	}
	res.Error = errText(out.Err)
	return res
}

func (r *Router) deliver(ctx context.Context, agent string, req localruntime.Request) localruntime.Result {
	runtimeName := r.cfg.Agents[agent].Runtime
	adapter, ok := r.adapters[runtimeName]
	if !ok {
		return localruntime.Result{Status: localruntime.Failed, Err: fmt.Errorf("runtime %q is not available", runtimeName)}
	}
	return adapter.Deliver(ctx, r.Target(agent), req)
}

func (r *Router) record(agent string, o Outcome) {
	o.At = time.Now().UTC()
	r.outcomeMu.Lock()
	r.outcomes[agent] = o
	r.outcomeMu.Unlock()
}

// AgentStatus is one agent in /status.
type AgentStatus struct {
	Name         string                    `json:"name"`
	Runtime      string                    `json:"runtime"`
	Workspace    string                    `json:"workspace"`
	Channels     []string                  `json:"channels,omitempty"`
	Presence     localruntime.Presence     `json:"presence"`
	Capabilities localruntime.Capabilities `json:"capabilities"`
	LastDelivery *Outcome                  `json:"last_delivery,omitempty"`
}

// Status reports every agent with its presence and last delivery.
func (r *Router) Status(ctx context.Context) []AgentStatus {
	names := make([]string, 0, len(r.cfg.Agents))
	for n := range r.cfg.Agents {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]AgentStatus, 0, len(names))
	for _, n := range names {
		a := r.cfg.Agents[n]
		st := AgentStatus{Name: n, Runtime: a.Runtime, Workspace: a.Workspace, Channels: a.Channels, Presence: localruntime.Unknown}
		if adapter, ok := r.adapters[a.Runtime]; ok {
			st.Presence = adapter.Presence(ctx, r.Target(n))
			st.Capabilities = adapter.Capabilities()
		}
		r.outcomeMu.RLock()
		if o, ok := r.outcomes[n]; ok {
			st.LastDelivery = &o
		}
		r.outcomeMu.RUnlock()
		out = append(out, st)
	}
	return out
}

func (r *Router) duplicate(id string) bool {
	if id == "" {
		return false
	}
	r.seenMu.Lock()
	defer r.seenMu.Unlock()
	if _, ok := r.seen[id]; ok {
		return true
	}
	now := time.Now()
	r.seen[id] = now
	if len(r.seen) > seenMax {
		for k, t := range r.seen {
			if now.Sub(t) > seenRetention {
				delete(r.seen, k)
			}
		}
	}
	return false
}

func messageID(data map[string]interface{}) string {
	if id := value(data, "message_id"); id != "" {
		return value(data, "channel") + ":" + id
	}
	return ""
}

func value(data map[string]interface{}, key string) string {
	v, ok := data[key]
	if !ok || v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func isToolInvocation(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	for _, prefix := range []string{"/tools", "/tool"} {
		if lower == prefix || strings.HasPrefix(lower, prefix+" ") {
			return true
		}
	}
	return false
}
