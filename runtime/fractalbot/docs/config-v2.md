# Configuration (version 2)

FractalBot's configuration has four concepts:

| Concept | Section | Answers |
| --- | --- | --- |
| Channel | `channels.<type>.<name>` | How to reach a chat platform: credentials, allowlists, polling |
| Agent | `agents.<name>` | Who receives: a workspace, one runtime, the channels it is bound to |
| Runtime | `runtimes` | How to deliver: tuning for Claude Desktop, Codex App, DeepSeek Harness and agent-manager |
| Schedule | `schedules.jobs.<id>` | When to wake an agent, and with what prompt |

The file holds no secret values and no runtime data paths. Secrets are `secret:<name>` references resolved on each machine. Inboxes, deduplication records, polling offsets and schedule state live under `dataDir` at fixed paths.

Version 1 files are not loaded. `fractalbot config check` names the version 2 replacement for each version 1 field it finds (see [Rewriting a version 1 file](#rewriting-a-version-1-file)).

## Example

```yaml
version: 2
dataDir: ~/.fractalbot          # inboxes, dedupe, polling offsets, schedule state
timezone: Asia/Shanghai

# ── Channels: how to reach each chat platform ───────────────
channels:
  feishu:
    xiaoyi:
      appId: cli_xxx
      appSecret: secret:feishu-xiaoyi
      allowedUsers: [ou_xxx]
    burry:
      appId: cli_yyy
      appSecret: secret:feishu-burry

  # Shared by the three agents: messages that name no one go to xiaoyi.
  telegram:
    main:
      botToken: secret:telegram-main
      allowedUsers: [5088760910]
      default: xiaoyi
      chats: {"-100123456789": burry}   # this group always goes to burry
  imessage:
    main:
      recipient: "+8600000000000"
      default: xiaoyi

# ── Agents: who receives messages and schedules ─────────────
agents:
  xiaoyi:                       # a Claude Desktop session (its start hook runs `fractalbot relay`)
    workspace: ~/xiaoyi-clawd
    runtime: claudeDesktop
    channels: [feishu.xiaoyi, telegram.main, imessage.main]

  burry:                        # a pinned Codex App thread
    workspace: ~/burry-claw
    runtime: codexApp
    session: 01a15696-d74e-7740-825f-911aee042f56
    channels: [feishu.burry, telegram.main, imessage.main]

  builder:                      # a tmux agent run by agent-manager
    workspace: ~/workspace/builder
    runtime: agentManager
    channels: [telegram.main, imessage.main]

# ── Schedules: when to wake which agent ─────────────────────
schedules:
  maxConcurrent: 3
  jobs:
    xiaoyi-heartbeat:
      agent: xiaoyi
      cron: "11 * * * *"
      backoff: {idle: "11 */3 * * *", deep-idle: "11 9 * * *"}
      resetOnInbound: true      # a message to xiaoyi restores hourly runs
      prompt: Read HEARTBEAT.md and follow it.

    burry-btc-risk-radar:
      agent: burry
      cron: "5 */2 * * *"
      promptFile: tasks/schedules/btc-risk-radar.md   # read on every run
```

## Top level

| Field | Default | Notes |
| --- | --- | --- |
| `version` | required | Must be `2`. |
| `dataDir` | per OS, see below | Absolute path or `~`. |
| `timezone` | system time zone | Default for schedules. Use an IANA name. |
| `gateway.bind`, `gateway.port` | `127.0.0.1`, `18789` | `gateway.allowedOrigins` limits WebSocket origins. |
| `inbound.ackEnabled`, `inbound.ackMessage` | `true`, `处理中…` | The acknowledgment sent before a message is delivered. |
| `secrets` | `store: auto` | Where this machine keeps secret values. See [Secrets](#secrets). |

## Channels

A configured instance is enabled. Each instance is referenced as `type.name`. Feishu supports several applications. Telegram, Slack, Discord, iMessage and demail have one instance each for now, which several agents can share.

| Type | Fields |
| --- | --- |
| `feishu` | `appId`, `appSecret` (secret), `domain` (`feishu` or `lark`), `allowedUsers` |
| `telegram` | `botToken` (secret), `allowedUsers`, `allowedChats`, `adminId`, `mode` (`polling` or `webhook`), `pollingTimeoutSeconds`, `pollingLimit`, `webhook` (`listenAddr`, `publicUrl`, `path`, `secretToken` (secret), `registerOnStart`, `deleteOnStop`) |
| `imessage` | `recipient`, `service`, `pollIntervalSeconds`, `pollLimit`, `databasePath`. macOS only. |
| `slack` | `botToken`, `appToken` (secrets), `allowedUsers`, `allowedChannels` |
| `discord` | `token` (secret), `allowedUsers` |
| `demail` | `graphqlUrl`, `packageId`, `address`, `identityKey` (secret), `sponsorAddress`, `gasCoin`, `pollIntervalSeconds`, `allowedSenders`, `peers` |

Every instance also accepts these sharing fields:

- `default`: the agent that receives messages that pick no one. It is required when several agents are bound to the channel.
- `chats`: pins a chat, group or conversation ID to one agent.

Every channel instance must be bound to at least one agent.

### Who receives a message

On a channel bound to one agent, that agent receives every message. On a shared channel, the receiver is chosen in this order:

1. **Mention.** `@burry …` at the start of the message, or `/agent burry …` / `/to burry …`. This applies to that one message only.
2. **Pinned chat.** The channel's `chats` entry for the conversation.
3. **`/use`.** After `/use burry`, the conversation goes to burry until `/use` is sent with no name. The choice survives restarts.
4. **Default.** The channel's `default`.

`/who` replies with the current receiver and the agents on the channel. Only agents bound to the channel can be chosen.

When several agents share a channel, an agent replies with `fractalbot message send --agent <name>`. The gateway then prefixes the text with `[name]`.

## Agents

| Field | Notes |
| --- | --- |
| `workspace` | Required, and must exist. `promptFile` paths and `fractalbot relay` are resolved against it. |
| `runtime` | `claudeDesktop`, `codexApp`, `deepseekHarness` or `agentManager`. |
| `channels` | Channel instances as `type.name`. An agent with no channels only runs schedules. |
| `session` | `codexApp`: the Codex thread ID (required). `deepseekHarness`: the DSH session ID (`session-<UUID>`, required). `agentManager`: the agent-manager agent (default `main`). `claudeDesktop`: none. |

An agent has one runtime. To send one workspace's messages to one runtime and its schedules to another, configure two agents with the same workspace (for example `xiaoyi` with `runtime: codexApp` and channels, and `xiaoyi-desktop` with `runtime: claudeDesktop` and only schedules).

## Runtimes

| Runtime | How it delivers | Tuning (`runtimes.<name>`) |
| --- | --- | --- |
| `claudeDesktop` | Writes the agent's inbox `<dataDir>/inbox/<agent>/`. `fractalbot relay`, started inside the agent's Claude Desktop session, moves each envelope into that session. | `deliveryTimeoutSeconds` |
| `codexApp` | Runs `codex queue --thread <session>` in the workspace. Codex's own queue holds the message until the thread is free. | `codex` (executable, default `codex` on PATH), `deliveryTimeoutSeconds` |
| `deepseekHarness` | Selects the configured DSH session under the configured workspace, verifies the selection, then submits through an explicitly enabled loopback CDP endpoint. It has no durable queue, so the app and endpoint must be available. | `cdpEndpoint` (required, loopback HTTP URL), `targetSelector`, `deliveryTimeoutSeconds` |
| `agentManager` | Runs the workspace's agent-manager `assign <session>`. | `script` (default `.claude/skills/agent-manager/scripts/main.py`), `assignTimeoutSeconds`, `startIfMissing` |

The inbox format is specified in [inbox-protocol.md](inbox-protocol.md).

To use `deepseekHarness`, fully quit DeepSeek Harness Desktop and launch it
with a local debugging port, for example:

```sh
open -a "DeepSeek Harness" --args --remote-debugging-port=9224
```

Then configure `runtimes.deepseekHarness.cdpEndpoint` as
`http://127.0.0.1:9224`, and set each Harness agent's `session` to its stable
DSH session ID (`session-<UUID>`). The runtime deliberately requires a
loopback endpoint, only accepts the app's `dsh-app://app` page, and verifies
that the selected session belongs to the agent's configured workspace before it
writes to a composer.

Start the relay from a Claude Desktop session's start hook:

```sh
fractalbot relay              # finds the agent whose workspace is the current directory
fractalbot relay --agent xiaoyi
```

## Schedules

| Field | Notes |
| --- | --- |
| `agent` | Required. The runtime and destination follow from the agent. |
| `cron` | Five-field cron in `timezone`. |
| `prompt` / `promptFile` | Exactly one. `promptFile` is relative to the agent's workspace and is read on every run. |
| `backoff` | Lower-frequency levels the agent may switch to with `fractalbot heartbeat cron set`. |
| `resetOnInbound` | A message to this job's agent restores `cron`. Other agents' messages do not. |
| `mode: heartbeatRun` | `agentManager` agents only. Runs agent-manager's heartbeat lifecycle and waits for its outcome. |
| `timezone`, `timeoutSeconds` | Optional overrides. |

The gateway appends to every scheduled prompt the commands for reporting an outcome and, when `backoff` is set, for changing the frequency. See [heartbeat.md](heartbeat.md).

## Secrets

Secret fields only accept `secret:<name>`; a plaintext value fails validation. The name uses lower-case letters, digits and hyphens. Each machine decides where values live:

```yaml
secrets:
  store: auto           # auto | os | file | env | command
  namespace: fractalbot # service name in the OS credential store
  command: [op, read]   # store: command; the secret name is appended
  overrides:
    telegram-main:
      command: [op, read, "op://Private/FractalBot Telegram/token"]
```

| Store | Where |
| --- | --- |
| `os` | macOS keychain, Windows Credential Manager or Linux Secret Service. Service is `namespace`, account is the secret name. |
| `file` | `<dataDir>/secrets/<name>`. The directory must be `0700` and the file `0600`, owned by you. |
| `env` | `FRACTALBOT_SECRET_<NAME>` (upper case, hyphens become underscores). Read only. |
| `command` | An external password manager. It runs without a shell and has 5 seconds. The first output line is the value; error output is never recorded. |

`auto` picks a store as follows:

- `env` in containers;
- `file` on Linux without a session bus;
- `os` everywhere else.

Commands:

```sh
fractalbot secret set feishu-xiaoyi    # reads the value from stdin (hidden on a terminal)
fractalbot secret list                 # name, store, set or missing, which channels use it
fractalbot secret check                # exits 1 if any is missing
fractalbot secret rm feishu-xiaoyi
```

The gateway resolves secrets at startup and keeps them only in memory.

- **Logs and errors are masked.** Every resolved value, and known token shapes such as Telegram bot tokens, are masked in logs and API errors.
- **A missing secret stops only its channel.** That channel stays stopped and `/status` gives the reason; the other channels still run.

## Data directory

| OS | Configuration file | `dataDir` default |
| --- | --- | --- |
| macOS | `~/.config/fractalbot/config.yaml` | `~/Library/Application Support/FractalBot` |
| Linux | `$XDG_CONFIG_HOME/fractalbot/config.yaml` | `$XDG_STATE_HOME/fractalbot` |
| Windows | `%APPDATA%\FractalBot\config.yaml` | `%LOCALAPPDATA%\FractalBot` |

```
<dataDir>/
├── inbox/<agent>/                  claudeDesktop agents' inboxes
├── state/channels/<type>-<name>.json  deduplication, polling offset or cursor
├── state/selections.json           /use choices
├── state/schedules.json            schedule state and reported outcomes
└── secrets/                        the file store
```

## Checking

```sh
fractalbot config check
```

`config check` validates the file and checks this machine. It then prints:

- where each channel goes;
- each agent's runtime, inbox or thread, and schedules;
- whether each secret is set (never the value);
- the state files.

It exits 1 on any problem. It checks that:

- every channel reaches an agent;
- shared channels have a valid `default`;
- workspaces and prompt files exist;
- cron expressions and time zones parse;
- the channels and runtimes work on this OS (iMessage needs macOS; `claudeDesktop`, `codexApp` and `deepseekHarness` need macOS or Windows; `agentManager` needs tmux, so it does not run on Windows outside WSL);
- the `codex` and `tmux` executables exist;
- every secret is set.

## Running as a service

```sh
fractalbot service install   # macOS launchd agent, Linux systemd user unit, Windows logon task
fractalbot service status
fractalbot reload            # restart so the gateway reads the configuration and secrets again
fractalbot service uninstall
```

The service starts this executable with this configuration file. It restarts the gateway when it exits and writes its log to `<dataDir>/logs/fractalbot.log`. The service definition holds no secrets.

On macOS, the Automation permission (iMessage controls Messages and System Events) and Full Disk Access belong to the executable's path. The service resolves symlinks and runs the real file, so upgrade by replacing that file rather than moving the service to a new path. A new path needs the permissions granted again; until then, iMessage fails to start with `AppleEvent timed out (-1712)`.

To rotate a secret:

1. Generate a new value on the platform.
2. Run `fractalbot secret set <name>`, then `fractalbot reload`.
3. Confirm the channel in `/status`, then revoke the old value.

## Rewriting a version 1 file

| Version 1 | Version 2 |
| --- | --- |
| `channels.feishu.bots.<name>` | `channels.feishu.<name>` |
| `channels.*.enabled`, singleton Feishu `appId` | a configured instance is enabled |
| `appSecret`, `botToken`, … in plain text | `secret:<name>`, then `fractalbot secret set <name>` |
| `dedupeFile`, `pollingOffsetFile`, `cursorFile`, `heartbeat.statePath`, `inboxPath`, `claudeDesktopInboxPath` | derived from `dataDir` |
| `agents.router`, global `agents.ohMyCode.workspace` fallback | each channel lists its agents; a channel without one is an error |
| `agents.ohMyCode.targets.<name>` with `receivers` | `agents.<name>` with `channels` |
| target `workspace`, `runtime` | `agents.<name>.workspace`, `runtime` |
| `agentManagerScript`, `assignTimeoutSeconds` | `runtimes.agentManager.script`, `assignTimeoutSeconds` |
| `agents.codexAppCDP` (CDP or inbox relay) and per-workspace `fractalbot-codex-app-route.py` | `runtime: codexApp` with `session: <thread ID>` |
| `agents.claudeDesktop` | `runtime: claudeDesktop` |
| `defaultAgent`, `allowedAgents` | `agents.<name>.session`; a channel reaches the agents bound to it |
| `agents.heartbeat.jobs[]` (`id`, `runtime`, `target`, `agent`, `text`) | `schedules.jobs.<id>` with `agent`, `prompt` or `promptFile` |
| `agentCronProfiles`, `resetCronOnInbound`, `dispatchMode` | `backoff`, `resetOnInbound`, `mode` |
| `demail.identityKeyFile` | `identityKey: secret:<name>` |
| `fractalbot claude-desktop relay --inbox-path …` | `fractalbot relay [--agent name]` |

After rewriting:

1. Move the old inboxes, deduplication files and Telegram offset to their `dataDir` paths.
2. Restart the gateway.
3. Change each Claude Desktop workspace's start hook to `fractalbot relay`.
