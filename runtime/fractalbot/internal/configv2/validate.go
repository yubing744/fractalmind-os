package configv2

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

var (
	namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

const (
	defaultBind              = "127.0.0.1"
	defaultPort              = 18789
	defaultAgentManagerAgent = "main"
	// DefaultAgentManagerScript is relative to the agent's workspace.
	DefaultAgentManagerScript = ".claude/skills/agent-manager/scripts/main.py"
	maxTimeoutSeconds         = 24 * 60 * 60
	modeHeartbeatRun          = "heartbeatRun"
)

// problems collects every validation error so one check reports them all.
type problems []string

func (ps *problems) add(format string, args ...any) { *ps = append(*ps, fmt.Sprintf(format, args...)) }

func (ps problems) err() error {
	if len(ps) == 0 {
		return nil
	}
	sorted := append([]string(nil), ps...)
	sort.Strings(sorted)
	return errors.New(strings.Join(sorted, "\n"))
}

// normalize fills defaults and resolves paths.
func (c *Config) normalize(p Platform) error {
	var ps problems
	if c.DataDir == "" {
		c.DataDir = p.DefaultDataDir()
	} else if dir, err := p.ExpandPath(c.DataDir); err != nil {
		ps.add("dataDir: %v", err)
	} else {
		c.DataDir = dir
	}
	c.Timezone = strings.TrimSpace(c.Timezone)
	if c.Gateway.Bind == "" {
		c.Gateway.Bind = defaultBind
	}
	if c.Gateway.Port == 0 {
		c.Gateway.Port = defaultPort
	}
	switch codex := c.Runtimes.CodexApp.Codex; {
	case codex == "":
		c.Runtimes.CodexApp.Codex = "codex"
	case strings.ContainsAny(codex, `/\~`):
		// A path rather than a name to look up on PATH.
		if expanded, err := p.ExpandPath(codex); err != nil {
			ps.add("runtimes.codexApp.codex: %v", err)
		} else {
			c.Runtimes.CodexApp.Codex = expanded
		}
	}
	if c.Runtimes.AgentManager.Script == "" {
		c.Runtimes.AgentManager.Script = DefaultAgentManagerScript
	}
	for name, a := range c.Agents {
		if a.Workspace != "" {
			if ws, err := p.ExpandPath(a.Workspace); err != nil {
				ps.add("agents.%s.workspace: %v", name, err)
			} else {
				a.Workspace = ws
			}
		}
		a.Session = strings.TrimSpace(a.Session)
		if a.Runtime == RuntimeAgentManager && a.Session == "" {
			a.Session = defaultAgentManagerAgent
		}
		c.Agents[name] = a
	}
	return ps.err()
}

func (c *Config) validate(p Platform) error {
	var ps problems
	if c.Gateway.Port < 1 || c.Gateway.Port > 65535 {
		ps.add("gateway.port: must be between 1 and 65535")
	}
	if c.Timezone != "" {
		if _, err := time.LoadLocation(c.Timezone); err != nil {
			ps.add("timezone: unknown time zone %q", c.Timezone)
		}
	}
	if err := c.Secrets.Validate(); err != nil {
		ps.add("%v", err)
	}
	c.validateRuntimes(&ps)
	if len(c.Agents) == 0 {
		ps.add("agents: at least one agent is required")
	}
	bound := c.validateAgents(&ps, p)
	c.validateChannels(&ps, p, bound)
	c.validateSchedules(&ps, p)
	return ps.err()
}

func (c *Config) validateRuntimes(ps *problems) {
	r := c.Runtimes
	if r.ClaudeDesktop.DeliveryTimeoutSeconds < 0 || r.CodexApp.DeliveryTimeoutSeconds < 0 || r.DeepSeekHarness.DeliveryTimeoutSeconds < 0 || r.AgentManager.AssignTimeoutSeconds < 0 {
		ps.add("runtimes: timeouts must not be negative")
	}
	if script := r.AgentManager.Script; strings.Contains(script, "..") {
		ps.add("runtimes.agentManager.script: must not contain ..")
	}
}

// validateAgents checks each agent and returns, for every channel instance,
// the agents bound to it.
func (c *Config) validateAgents(ps *problems, p Platform) map[string][]string {
	bound := map[string][]string{}
	instances := c.channelInstances()
	for _, name := range sortedKeys(c.Agents) {
		a := c.Agents[name]
		prefix := "agents." + name
		if !namePattern.MatchString(name) {
			ps.add("%s: agent names use lower-case letters, digits, hyphens and underscores", prefix)
		}
		switch a.Runtime {
		case RuntimeClaudeDesktop, RuntimeCodexApp, RuntimeDeepSeekHarness, RuntimeAgentManager:
			if reason := p.runtimeSupport(a.Runtime); reason != "" {
				ps.add("%s.runtime: %s", prefix, reason)
			}
		case "":
			ps.add("%s.runtime: required (claudeDesktop, codexApp, deepseekHarness or agentManager)", prefix)
		default:
			ps.add("%s.runtime: unsupported runtime %q (use claudeDesktop, codexApp, deepseekHarness or agentManager)", prefix, a.Runtime)
		}
		if a.Workspace == "" {
			ps.add("%s.workspace: required", prefix)
		} else if !p.DirExists(a.Workspace) {
			ps.add("%s.workspace: %s is not a directory", prefix, a.Workspace)
		}
		switch a.Runtime {
		case RuntimeCodexApp:
			if !uuidPattern.MatchString(a.Session) {
				ps.add("%s.session: codexApp agents need the Codex thread ID (a lower-case UUID)", prefix)
			}
		case RuntimeClaudeDesktop:
			if a.Session != "" {
				ps.add("%s.session: claudeDesktop agents have no session", prefix)
			}
		case RuntimeDeepSeekHarness:
			if a.Session != "" {
				ps.add("%s.session: deepseekHarness delivers to the active app composer and has no session", prefix)
			}
			if err := validateDeepSeekHarnessEndpoint(c.Runtimes.DeepSeekHarness.CDPEndpoint); err != nil {
				ps.add("runtimes.deepseekHarness.cdpEndpoint: %v", err)
			}
		case RuntimeAgentManager:
			if !namePattern.MatchString(a.Session) {
				ps.add("%s.session: %q is not a valid agent-manager agent name", prefix, a.Session)
			}
		}
		seen := map[string]bool{}
		for _, ref := range a.Channels {
			if seen[ref] {
				ps.add("%s.channels: %s is listed twice", prefix, ref)
				continue
			}
			seen[ref] = true
			if !instances[ref] {
				ps.add("%s.channels: %s is not configured under channels (write type.name)", prefix, ref)
				continue
			}
			bound[ref] = append(bound[ref], name)
		}
	}
	return bound
}

func validateDeepSeekHarnessEndpoint(raw string) error {
	endpoint := strings.TrimSpace(raw)
	if endpoint == "" {
		return errors.New("required when an agent uses deepseekHarness")
	}
	parsed, err := url.ParseRequestURI(endpoint)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil {
		return errors.New("must be an unauthenticated http loopback URL")
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "127.0.0.1", "::1", "localhost":
		return nil
	default:
		return errors.New("must use localhost, 127.0.0.1, or ::1")
	}
}

// channelInstances returns every configured `type.name`.
func (c *Config) channelInstances() map[string]bool {
	out := map[string]bool{}
	for _, inst := range c.ChannelList() {
		out[inst.Ref] = true
	}
	return out
}

func (c *Config) validateChannels(ps *problems, p Platform, bound map[string][]string) {
	for _, inst := range c.ChannelList() {
		prefix := "channels." + inst.Ref
		if !namePattern.MatchString(inst.Name) {
			ps.add("%s: channel names use lower-case letters, digits, hyphens and underscores", prefix)
		}
		if reason := p.channelSupport(inst.Type); reason != "" {
			ps.add("%s: %s", prefix, reason)
		}
		agents := bound[inst.Ref]
		boundTo := func(agent string) bool {
			for _, a := range agents {
				if a == agent {
					return true
				}
			}
			return false
		}
		switch {
		case len(agents) == 0:
			ps.add("%s: no agent receives this channel; add it to an agent's channels or remove it", prefix)
		case len(agents) > 1 && inst.Sharing.Default == "":
			ps.add("%s.default: required because %s share this channel", prefix, strings.Join(agents, ", "))
		}
		if d := inst.Sharing.Default; d != "" && len(agents) > 0 && !boundTo(d) {
			ps.add("%s.default: %s is not bound to this channel", prefix, d)
		}
		for _, chat := range sortedKeys(inst.Sharing.Chats) {
			if agent := inst.Sharing.Chats[chat]; !boundTo(agent) {
				ps.add("%s.chats[%q]: %s is not bound to this channel", prefix, chat, agent)
			}
		}
	}
	if len(c.Channels.IMessage) > 1 {
		ps.add("channels.imessage: a Mac has one iMessage account; configure one instance and share it")
	}
	for kind, n := range map[string]int{ChannelTelegram: len(c.Channels.Telegram), ChannelSlack: len(c.Channels.Slack), ChannelDiscord: len(c.Channels.Discord), ChannelDemail: len(c.Channels.Demail)} {
		if n > 1 {
			ps.add("channels.%s: only one instance is supported for now; share it between agents", kind)
		}
	}
	apps := map[string]string{}
	for _, name := range sortedKeys(c.Channels.Feishu) {
		id := c.Channels.Feishu[name].AppID
		if other, ok := apps[id]; ok && id != "" {
			ps.add("channels.feishu.%s: appId is also used by channels.feishu.%s", name, other)
		}
		apps[id] = name
	}
	for _, name := range sortedKeys(c.Channels.Feishu) {
		f := c.Channels.Feishu[name]
		if f.AppID == "" || f.AppSecret == "" {
			ps.add("channels.feishu.%s: appId and appSecret are required", name)
		}
		if f.Domain != "" && f.Domain != "feishu" && f.Domain != "lark" {
			ps.add("channels.feishu.%s.domain: use feishu or lark", name)
		}
	}
	for _, name := range sortedKeys(c.Channels.Telegram) {
		t := c.Channels.Telegram[name]
		if t.BotToken == "" {
			ps.add("channels.telegram.%s.botToken: required", name)
		}
		switch t.Mode {
		case "", "polling":
			if t.Webhook != nil {
				ps.add("channels.telegram.%s.webhook: only used with mode: webhook", name)
			}
		case "webhook":
			if t.Webhook == nil || t.Webhook.ListenAddr == "" || t.Webhook.PublicURL == "" {
				ps.add("channels.telegram.%s.webhook: listenAddr and publicUrl are required with mode: webhook", name)
			}
		default:
			ps.add("channels.telegram.%s.mode: use polling or webhook", name)
		}
	}
	for _, name := range sortedKeys(c.Channels.IMessage) {
		if c.Channels.IMessage[name].Recipient == "" {
			ps.add("channels.imessage.%s.recipient: required", name)
		}
	}
	for _, name := range sortedKeys(c.Channels.Slack) {
		s := c.Channels.Slack[name]
		if s.BotToken == "" || s.AppToken == "" {
			ps.add("channels.slack.%s: botToken and appToken are required", name)
		}
	}
	for _, name := range sortedKeys(c.Channels.Discord) {
		if c.Channels.Discord[name].Token == "" {
			ps.add("channels.discord.%s.token: required", name)
		}
	}
	for _, name := range sortedKeys(c.Channels.Demail) {
		d := c.Channels.Demail[name]
		if d.GraphQLURL == "" || d.PackageID == "" || d.Address == "" || d.IdentityKey == "" {
			ps.add("channels.demail.%s: graphqlUrl, packageId, address and identityKey are required", name)
		}
	}
}

func (c *Config) validateSchedules(ps *problems, p Platform) {
	if c.Schedules.MaxConcurrent < 0 {
		ps.add("schedules.maxConcurrent: must not be negative")
	}
	for _, id := range sortedKeys(c.Schedules.Jobs) {
		j := c.Schedules.Jobs[id]
		prefix := "schedules.jobs." + id
		if !namePattern.MatchString(id) {
			ps.add("%s: job IDs use lower-case letters, digits, hyphens and underscores", prefix)
		}
		agent, ok := c.Agents[j.Agent]
		if !ok {
			ps.add("%s.agent: %q is not a configured agent", prefix, j.Agent)
		}
		tz := c.JobTimezone(j)
		if _, err := time.LoadLocation(tz); err != nil {
			ps.add("%s.timezone: unknown time zone %q", prefix, tz)
		} else if _, err := ParseCron(j.Cron, tz); err != nil {
			ps.add("%s.cron: %v", prefix, err)
		}
		for _, level := range sortedKeys(j.Backoff) {
			if !namePattern.MatchString(level) {
				ps.add("%s.backoff: %q is not a valid level name", prefix, level)
			} else if _, err := ParseCron(j.Backoff[level], tz); err != nil {
				ps.add("%s.backoff.%s: %v", prefix, level, err)
			}
		}
		switch j.Mode {
		case "":
			if (j.Prompt == "") == (j.PromptFile == "") {
				ps.add("%s: set exactly one of prompt and promptFile", prefix)
			}
		case modeHeartbeatRun:
			if ok && agent.Runtime != RuntimeAgentManager {
				ps.add("%s.mode: heartbeatRun needs an agentManager agent", prefix)
			}
			if j.Prompt != "" && j.PromptFile != "" {
				ps.add("%s: set at most one of prompt and promptFile", prefix)
			}
		default:
			ps.add("%s.mode: unsupported mode %q", prefix, j.Mode)
		}
		if j.PromptFile != "" {
			rel := path.Clean(strings.ReplaceAll(j.PromptFile, `\`, "/"))
			switch {
			case path.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../"):
				ps.add("%s.promptFile: must be a path inside the agent's workspace", prefix)
			case ok && agent.Workspace != "" && !p.FileExists(c.PromptFilePath(j)):
				ps.add("%s.promptFile: %s does not exist", prefix, c.PromptFilePath(j))
			}
		}
		if j.TimeoutSeconds < 0 || j.TimeoutSeconds > maxTimeoutSeconds {
			ps.add("%s.timeoutSeconds: must be between 0 and %d", prefix, maxTimeoutSeconds)
		}
	}
}

// ParseCron parses a five-field expression in a time zone.
func ParseCron(expression, timezone string) (cron.Schedule, error) {
	return cron.ParseStandard("CRON_TZ=" + timezone + " " + strings.TrimSpace(expression))
}

// JobTimezone is the job's own time zone, else the configuration's, else the
// system's.
func (c *Config) JobTimezone(j Job) string {
	switch {
	case j.Timezone != "":
		return j.Timezone
	case c.Timezone != "":
		return c.Timezone
	default:
		return "Local"
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
