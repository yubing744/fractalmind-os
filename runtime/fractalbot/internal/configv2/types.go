// Package configv2 is FractalBot's version 2 configuration: channels (how to
// reach a chat platform), agents (who receives: a workspace, one runtime and
// the channels it is bound to), runtimes (how to deliver) and schedules (when
// to wake an agent). It holds no secret values and no runtime data paths;
// secrets are `secret:<name>` references and data paths derive from dataDir.
// Version 1 configurations are rejected, not translated.
package configv2

import (
	"github.com/fractalmind-labs/fractalmind-os/runtime/fractalbot/internal/secrets"
	"gopkg.in/yaml.v3"
)

// Version is the only configuration version this package accepts.
const Version = 2

// Runtime names.
const (
	RuntimeClaudeDesktop = "claudeDesktop"
	RuntimeCodexApp      = "codexApp"
	RuntimeDeepSeekHarness = "deepseekHarness"
	RuntimeAgentManager  = "agentManager"
)

// Channel types.
const (
	ChannelFeishu   = "feishu"
	ChannelTelegram = "telegram"
	ChannelIMessage = "imessage"
	ChannelSlack    = "slack"
	ChannelDiscord  = "discord"
	ChannelDemail   = "demail"
)

// ChannelTypes lists every channel type in a stable order.
var ChannelTypes = []string{ChannelFeishu, ChannelTelegram, ChannelIMessage, ChannelSlack, ChannelDiscord, ChannelDemail}

// Config is a loaded, validated version 2 configuration. Paths are absolute.
type Config struct {
	Version  int    `yaml:"version"`
	DataDir  string `yaml:"dataDir,omitempty"`
	Timezone string `yaml:"timezone,omitempty"`

	Gateway   Gateway          `yaml:"gateway,omitempty"`
	Inbound   Inbound          `yaml:"inbound,omitempty"`
	Channels  Channels         `yaml:"channels,omitempty"`
	Secrets   secrets.Config   `yaml:"secrets,omitempty"`
	Runtimes  Runtimes         `yaml:"runtimes,omitempty"`
	Agents    map[string]Agent `yaml:"agents,omitempty"`
	Schedules Schedules        `yaml:"schedules,omitempty"`

	// Path is the file this configuration was loaded from.
	Path string `yaml:"-"`
}

// Gateway is the local HTTP and WebSocket endpoint.
type Gateway struct {
	Bind           string   `yaml:"bind,omitempty"`
	Port           int      `yaml:"port,omitempty"`
	AllowedOrigins []string `yaml:"allowedOrigins,omitempty"`
}

// Inbound controls the acknowledgment sent before a message is delivered.
type Inbound struct {
	// AckEnabled defaults to true.
	AckEnabled *bool  `yaml:"ackEnabled,omitempty"`
	AckMessage string `yaml:"ackMessage,omitempty"`
}

// SecretRef is a `secret:<name>` reference. Decoding rejects a plaintext
// value without echoing it.
type SecretRef string

// Name is the referenced secret's name.
func (r SecretRef) Name() string {
	name, _ := secrets.ParseRef(string(r))
	return name
}

// UnmarshalYAML accepts only a valid reference.
func (r *SecretRef) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return &fieldError{line: node.Line, msg: "must be a secret reference (secret:<name>)"}
	}
	if _, err := secrets.ParseRef(node.Value); err != nil {
		return &fieldError{line: node.Line, msg: err.Error()}
	}
	*r = SecretRef(node.Value)
	return nil
}

// Sharing is common to every channel instance. A channel bound to more than
// one agent is shared: each message picks one receiver by explicit mention,
// then a pinned chat, then the conversation's /use selection, then Default.
type Sharing struct {
	// Default receives messages on a shared channel that select no agent.
	Default string `yaml:"default,omitempty"`
	// Chats pins a chat, contact or conversation ID to one agent.
	Chats map[string]string `yaml:"chats,omitempty"`
}

// Channels holds channel instances by type, then by instance name. A
// configured instance is enabled.
type Channels struct {
	Feishu   map[string]Feishu   `yaml:"feishu,omitempty"`
	Telegram map[string]Telegram `yaml:"telegram,omitempty"`
	IMessage map[string]IMessage `yaml:"imessage,omitempty"`
	Slack    map[string]Slack    `yaml:"slack,omitempty"`
	Discord  map[string]Discord  `yaml:"discord,omitempty"`
	Demail   map[string]Demail   `yaml:"demail,omitempty"`
}

// Feishu is one Feishu or Lark application.
type Feishu struct {
	Sharing      `yaml:",inline"`
	AppID        string    `yaml:"appId"`
	AppSecret    SecretRef `yaml:"appSecret"`
	Domain       string    `yaml:"domain,omitempty"` // feishu (default) or lark
	AllowedUsers []string  `yaml:"allowedUsers,omitempty"`
}

// Telegram is one Telegram bot.
type Telegram struct {
	Sharing               `yaml:",inline"`
	BotToken              SecretRef `yaml:"botToken"`
	AllowedUsers          []int64   `yaml:"allowedUsers,omitempty"`
	AllowedChats          []int64   `yaml:"allowedChats,omitempty"`
	AdminID               int64     `yaml:"adminId,omitempty"`
	Mode                  string    `yaml:"mode,omitempty"` // polling (default) or webhook
	PollingTimeoutSeconds int       `yaml:"pollingTimeoutSeconds,omitempty"`
	PollingLimit          int       `yaml:"pollingLimit,omitempty"`
	Webhook               *Webhook  `yaml:"webhook,omitempty"`
}

// Webhook receives Telegram updates over HTTPS.
type Webhook struct {
	ListenAddr      string    `yaml:"listenAddr"`
	Path            string    `yaml:"path,omitempty"`
	PublicURL       string    `yaml:"publicUrl"`
	SecretToken     SecretRef `yaml:"secretToken,omitempty"`
	RegisterOnStart bool      `yaml:"registerOnStart,omitempty"`
	DeleteOnStop    bool      `yaml:"deleteOnStop,omitempty"`
}

// IMessage is the Mac's iMessage account. macOS only.
type IMessage struct {
	Sharing             `yaml:",inline"`
	Recipient           string `yaml:"recipient"`
	Service             string `yaml:"service,omitempty"`
	PollIntervalSeconds int    `yaml:"pollIntervalSeconds,omitempty"`
	PollLimit           int    `yaml:"pollLimit,omitempty"`
	DatabasePath        string `yaml:"databasePath,omitempty"`
}

// Slack is one Slack app in Socket Mode.
type Slack struct {
	Sharing         `yaml:",inline"`
	BotToken        SecretRef `yaml:"botToken"`
	AppToken        SecretRef `yaml:"appToken"`
	AllowedUsers    []string  `yaml:"allowedUsers,omitempty"`
	AllowedChannels []string  `yaml:"allowedChannels,omitempty"`
}

// Discord is one Discord bot.
type Discord struct {
	Sharing      `yaml:",inline"`
	Token        SecretRef `yaml:"token"`
	AllowedUsers []string  `yaml:"allowedUsers,omitempty"`
}

// Demail is one on-chain mail identity on Sui.
type Demail struct {
	Sharing             `yaml:",inline"`
	GraphQLURL          string            `yaml:"graphqlUrl"`
	PackageID           string            `yaml:"packageId"`
	Address             string            `yaml:"address"`
	IdentityKey         SecretRef         `yaml:"identityKey"`
	SponsorAddress      string            `yaml:"sponsorAddress,omitempty"`
	GasCoin             string            `yaml:"gasCoin,omitempty"`
	PollIntervalSeconds int               `yaml:"pollIntervalSeconds,omitempty"`
	AllowedSenders      []string          `yaml:"allowedSenders,omitempty"`
	Peers               map[string]string `yaml:"peers,omitempty"`
}

// Runtimes tunes how each runtime delivers. A runtime is used when an agent
// names it; this section only changes defaults.
type Runtimes struct {
	ClaudeDesktop ClaudeDesktopRuntime `yaml:"claudeDesktop,omitempty"`
	CodexApp      CodexAppRuntime      `yaml:"codexApp,omitempty"`
	DeepSeekHarness DeepSeekHarnessRuntime `yaml:"deepseekHarness,omitempty"`
	AgentManager  AgentManagerRuntime  `yaml:"agentManager,omitempty"`
}

// ClaudeDesktopRuntime delivers through the agent's inbox, which the relay in
// the agent's Claude Desktop session consumes.
type ClaudeDesktopRuntime struct {
	DeliveryTimeoutSeconds int `yaml:"deliveryTimeoutSeconds,omitempty"`
}

// CodexAppRuntime queues into a pinned Codex App thread with `codex queue`.
type CodexAppRuntime struct {
	// Codex is the codex executable. Default: `codex` on PATH.
	Codex                  string `yaml:"codex,omitempty"`
	DeliveryTimeoutSeconds int    `yaml:"deliveryTimeoutSeconds,omitempty"`
}

// DeepSeekHarnessRuntime delivers to the active DeepSeek Harness Desktop
// composer through its locally exposed Chromium DevTools endpoint. The app
// must be launched with a loopback remote-debugging port; FractalBot never
// enables remote debugging itself.
type DeepSeekHarnessRuntime struct {
	// CDPEndpoint is the local Chromium DevTools endpoint, for example
	// http://127.0.0.1:9224.
	CDPEndpoint string `yaml:"cdpEndpoint,omitempty"`
	// TargetSelector optionally matches the DeepSeek Harness page title or URL.
	TargetSelector string `yaml:"targetSelector,omitempty"`
	DeliveryTimeoutSeconds int `yaml:"deliveryTimeoutSeconds,omitempty"`
}

// AgentManagerRuntime assigns to tmux agents through agent-manager.
type AgentManagerRuntime struct {
	// Script is relative to the agent's workspace unless absolute. Default:
	// .claude/skills/agent-manager/scripts/main.py.
	Script               string `yaml:"script,omitempty"`
	AssignTimeoutSeconds int    `yaml:"assignTimeoutSeconds,omitempty"`
	// StartIfMissing starts a stopped agent before a schedule delivers to it.
	StartIfMissing bool `yaml:"startIfMissing,omitempty"`
}

// Agent is one message and schedule receiver.
type Agent struct {
	Workspace string `yaml:"workspace"`
	Runtime   string `yaml:"runtime"`
	// Channels are channel instances as `type.name`.
	Channels []string `yaml:"channels,omitempty"`
	// Session is the receiver inside the runtime: the agent-manager agent
	// (default main) or the Codex App thread ID (required). Claude Desktop
	// agents have none.
	Session string `yaml:"session,omitempty"`
}

// Schedules wake agents on cron schedules.
type Schedules struct {
	MaxConcurrent int            `yaml:"maxConcurrent,omitempty"`
	Jobs          map[string]Job `yaml:"jobs,omitempty"`
}

// Job is one schedule. Its runtime and destination follow from Agent.
type Job struct {
	Agent string `yaml:"agent"`
	Cron  string `yaml:"cron"`
	// Timezone overrides the top-level timezone.
	Timezone string `yaml:"timezone,omitempty"`
	// Exactly one of Prompt and PromptFile. PromptFile is relative to the
	// agent's workspace and read on every run.
	Prompt     string `yaml:"prompt,omitempty"`
	PromptFile string `yaml:"promptFile,omitempty"`
	// Backoff names the lower-frequency schedules the agent may switch to.
	Backoff map[string]string `yaml:"backoff,omitempty"`
	// ResetOnInbound restores Cron when this job's agent receives a message.
	ResetOnInbound bool `yaml:"resetOnInbound,omitempty"`
	// Mode heartbeatRun runs agent-manager's own heartbeat lifecycle and
	// waits for its outcome. agentManager agents only.
	Mode           string `yaml:"mode,omitempty"`
	TimeoutSeconds int    `yaml:"timeoutSeconds,omitempty"`
}
