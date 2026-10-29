# FractalBot 中文快速上手指南

FractalBot 是一个用 Go 编写的**多渠道 AI Agent 网关**，支持 Telegram、Slack、Discord、飞书（Feishu/Lark）等即时通讯平台。你可以通过它把消息路由到后端的 Agent（如 Claude Code），实现「手机发消息 → Agent 自动干活 → 手机收结果」的工作流。

**核心特性：**

- 本地优先 — 跑在你自己的机器上，完全可控
- 多 Agent — 可同时管理多个 Agent（qa、dev、sre 等），按需分配任务
- 多渠道 — Telegram / Slack / Discord / 飞书，一套配置搞定
- 安全白名单 — 仅允许指定用户与 Bot 交互

## 目录

- [架构](#架构)
- [环境准备](#环境准备)
- [安装](#安装)
- [5 分钟快速入门](#5-分钟快速入门)
- [Slack 10 分钟直通（推荐新手）](#slack-10-分钟直通推荐新手)
- [渠道配置详解](#渠道配置详解)
- [多 Agent 架构](#多-agent-架构agent-manager-详解)
- [启动与验证](#启动与验证)
- [常用命令速查](#常用命令速查)
- [FAQ](#faq)
- [附录：从 OpenClaw 迁移](#附录从-openclaw-迁移)
- [更多资源](#更多资源)

## 架构

```
            你的手机/电脑
               │
    ┌──────────┴──────────┐
    │  Telegram / Slack   │
    │  Discord  / 飞书    │
    └──────────┬──────────┘
               │ (消息)
    ┌──────────▼──────────┐
    │   FractalBot 网关   │
    │   (Go WebSocket)    │
    │   127.0.0.1:18789   │
    └──────────┬──────────┘
               │ (路由)
    ┌──────────▼──────────┐
    │   agent-manager     │
    │  (tmux + Claude)    │
    │                     │
    │  ┌───┐ ┌───┐ ┌───┐ │
    │  │qa │ │dev│ │...│  │
    │  └───┘ └───┘ └───┘  │
    └─────────────────────┘
```

## 环境准备

| 依赖 | 版本要求 | 用途 |
|------|---------|------|
| Go | 1.23+ | 编译 FractalBot（手动编译时需要） |
| git | 任意 | 克隆仓库 |
| python3 | 3.10+ | agent-manager 脚本 |
| tmux | 任意 | Agent 会话管理 |

macOS：

```bash
brew install go git python3 tmux
```

Ubuntu/Debian：

```bash
sudo apt update && sudo apt install -y golang git python3 tmux
```

## 安装

### 方式一：一键脚本（推荐）

```bash
curl -fsSL https://raw.githubusercontent.com/fractalmind-labs/fractalmind-os/main/runtime/fractalbot/install.sh | bash
```

安装完成后，二进制文件位于 `~/.local/bin/fractalbot`，默认配置在 `~/.config/fractalbot/config.yaml`。

> 如需指定版本：`curl -fsSL .../install.sh | FRACTALBOT_REF=v0.2.0 bash`

Linux 用户可追加 `--systemd-user` 自动注册为 systemd 用户服务：

```bash
curl -fsSL https://raw.githubusercontent.com/fractalmind-labs/fractalmind-os/main/runtime/fractalbot/install.sh | bash -s -- --systemd-user
```

### 方式二：手动编译

```bash
git clone https://github.com/fractalmind-labs/fractalmind-os.git
cd fractalmind-os/runtime/fractalbot
go build -o fractalbot ./cmd/fractalbot
./fractalbot --help   # 验证安装成功
```

> **命令约定：** 一键安装使用 `fractalbot`；手动编译并在当前目录运行时使用 `./fractalbot`。下文默认写 `fractalbot`，手动编译用户可按需替换。

---

## 5 分钟快速入门

> 用 **Slack + 一个 Agent** 搭出最小可用系统。
> 想用 Telegram / Discord / 飞书？先跑通这个流程，再参照下方「渠道配置详解」切换。

### Step 1 — 创建配置

```bash
mkdir -p ~/.config/fractalbot && cat > ~/.config/fractalbot/config.yaml << 'EOF'
version: 2

channels:
  slack:
    main:
      botToken: secret:slack-bot         # 值不写在配置里，见下一步
      appToken: secret:slack-app
      allowedUsers:
        - "U0YOUR_ID"                    # 替换为你的 Slack User ID

agents:
  dev:
    workspace: /path/to/your/workspace   # 替换为你的工作仓库路径
    runtime: agentManager                # 也可以是 claudeDesktop、codexApp 或 deepseekHarness
    session: dev                         # agent-manager 里的 Agent 名
    channels: [slack.main]
EOF

# 密钥存到本机（macOS 钥匙串 / Windows 凭据管理器 / Linux 文件），输入时不回显
fractalbot secret set slack-bot   # 粘贴 xoxb- token
fractalbot secret set slack-app   # 粘贴 xapp- token
fractalbot config check           # 校验配置、确认密钥已设置
```

> **获取 Slack token：** 见下方 [Slack 配置](#slack-socket-mode无需公网端口) 详细步骤。
> **不知道 User ID？** 先随便填，启动后给 Bot 发 `/whoami` 获取后再改。

### Step 2 — 创建最小 Agent

在你的工作仓库中：

```bash
cd /path/to/your/workspace

# 安装必需的 skills
npx openskills install agent-manager
npx openskills install use-fractalbot

# 创建 Agent 定义
mkdir -p agents
cat > agents/EMP_0001.md << 'EOF'
---
name: dev
role: developer
description: "dev — 开发 Agent"
working_directory: ${REPO_ROOT}
launcher: claude
launcher_args:
  - --dangerously-skip-permissions
skills:
  - agent-manager
  - use-fractalbot
---

# DEV AGENT
- 接收任务，完成开发
EOF
```

> **`--dangerously-skip-permissions` 安全提示：** 此参数让 Agent 无需用户确认即可执行任意命令。仅在你信任运行环境时使用。生产环境建议去掉此参数。

### Step 3 — 启动

```bash
fractalbot --config ~/.config/fractalbot/config.yaml
```

### Step 4 — 测试

在 Slack 中 DM 你的 Bot：

| 发送 | 预期结果 |
|------|----------|
| `/whoami` | 回复你的 User ID 和 Channel ID（示例：`User: U08XXXXXX`） |
| `/agents` | 显示 `dev` |
| `说 hello world` | Agent 接收任务并回复（示例：`[dev] hello world`） |

**成功标准：** Bot 启动无报错 + `/whoami` 回复正常 + 普通消息被路由到 dev Agent。

---

## Slack 10 分钟直通（推荐新手）

> 目标：只配置最小权限，先跑通 Slack DM 单链路（消息能进、回复能出）。

1. 在 [Slack API](https://api.slack.com/apps) 创建 App（推荐用 **From an app manifest**）
2. 在 **Settings → Socket Mode** 启用 Socket Mode
3. 在 **Basic Information → App-Level Tokens** 创建 `xapp-` token（scope: `connections:write`）
4. 在 **OAuth & Permissions** 确认 Bot Scopes 仅包含：`chat:write`、`im:history`、`im:read`
5. 在 **Event Subscriptions** 添加 Bot Event：`message.im`
6. 点 **Install App / Reinstall to Workspace**，获取 `xoxb-` token
7. 写入 `config.yaml` 并启动，依次测试：`/whoami` → `/ping` → `hello`

> **必须重装：** 只要改了 scopes 或 events，必须执行 **Reinstall to Workspace**。

<details>
<summary><b>Slack Manifest（最小可用）</b></summary>

```yaml
display_information:
  name: fractalbot
features:
  bot_user:
    display_name: Neo
    always_online: false
oauth_config:
  scopes:
    bot:
      - chat:write
      - im:history
      - im:read
settings:
  event_subscriptions:
    bot_events:
      - message.im
  org_deploy_enabled: false
  socket_mode_enabled: true
  token_rotation_enabled: false
```

> 导入 Manifest 后，仍需手动创建 App-Level Token（`connections:write`）。

</details>

---

## 渠道配置详解

> 选择**一个渠道**配置即可。推荐从 Slack 或 Telegram 开始。

### 如何获取 User ID

各渠道的 `allowedUsers` 需要填入你的用户 ID：

| 渠道 | 获取方式 |
|------|----------|
| Telegram | 给 Bot 发 `/whoami`；或使用 [@userinfobot](https://t.me/userinfobot) |
| Slack | 给 Bot 发 `/whoami`；或在 Slack 中点击自己头像 → Profile → 更多 → Copy member ID |
| Discord | 开启开发者模式（Settings → Advanced → Developer Mode），右键头像 → Copy User ID |
| 飞书 | 在飞书管理后台查看，或给 Bot 发 `/whoami` |

> 第一次配置时可以先任意填写 `allowedUsers`，启动后用 `/whoami` 获取真实 ID 再更新。

### Telegram（Polling 模式，本地开发推荐）

1. 在 Telegram 找 [@BotFather](https://t.me/BotFather)，发送 `/newbot` 创建 Bot，获取 `botToken`
2. 发送 `/mybots` → 选你的 Bot → Bot Settings → Group Privacy → **Turn off**（仅 DM 模式可跳过）
3. 填入配置：

```yaml
channels:
  telegram:
    main:
      botToken: secret:telegram-main     # fractalbot secret set telegram-main
      adminId: 5088760910                # 你的 Telegram User ID
      allowedUsers:
        - 5088760910                     # 白名单，同 adminId
      pollingTimeoutSeconds: 25          # 默认 polling 模式
```

### Slack（Socket Mode，无需公网端口）

1. 前往 [Slack API](https://api.slack.com/apps) 创建 App（推荐 Manifest 导入）
2. 开启 **Socket Mode**（Settings → Socket Mode → Enable）
3. 创建 App-Level Token（`connections:write`），获取 `xapp-` token
4. 在 OAuth & Permissions 配置 Bot Scopes（见下方“权限分层建议”）
5. 在 Event Subscriptions → Subscribe to bot events，添加 `message.im`
6. 安装 App 到 Workspace，获取 `xoxb-` token
7. **重要：** 修改 scopes 或 events 后，必须执行 Reinstall to Workspace

```yaml
channels:
  slack:
    main:
      botToken: secret:slack-bot         # fractalbot secret set slack-bot
      appToken: secret:slack-app         # fractalbot secret set slack-app
      allowedUsers:
        - "U08C93FU222"                  # Slack User ID
```

**权限分层建议：**

| 档位 | Bot Scopes | Bot Events | 适用场景 |
|------|------------|------------|----------|
| 最小可用（推荐起步） | `chat:write`, `im:history`, `im:read` | `message.im` | 仅 DM 对话 |
| 增强能力（按需添加） | `channels:history`, `groups:history`, `mpim:history`, `users:read`, `users:read.email` 等 | `app_mention`, `message.channels` 等 | 群聊、@提及、用户信息查询 |

<details>
<summary><b>Slack 配置检查清单</b></summary>

- [ ] `botToken` 以 `xoxb-` 开头
- [ ] `appToken` 以 `xapp-` 开头
- [ ] Socket Mode 已在 Slack App 设置中启用
- [ ] Bot Token Scopes 包含：`chat:write`、`im:history`、`im:read`
- [ ] Event Subscriptions 中已添加 `message.im`
- [ ] 修改 scopes/events 后已重新安装 App 到 Workspace
- [ ] `allowedUsers` 中填入了你的 Slack User ID

> **常见失败原因：** scopes 或 events 修改后忘记重新安装 App（Reinstall）。

</details>

### Discord

1. 前往 [Discord Developer Portal](https://discord.com/developers/applications) 创建 Application
2. 进入 Bot 页面，点击 Reset Token 获取 token
3. 开启 **Message Content Intent**
4. 用 OAuth2 URL Generator 邀请 Bot 到你的服务器（Scopes: `bot`；Permissions: `Send Messages`）

```yaml
channels:
  discord:
    main:
      token: secret:discord              # fractalbot secret set discord
      allowedUsers:
        - "123456789012345678"           # Discord User ID
```

### 飞书/Lark

1. 前往[飞书开放平台](https://open.feishu.cn/)创建应用
2. 获取 App ID 和 App Secret

```yaml
channels:
  feishu:
    assistant:                           # 实例名；多个飞书应用各写一段
      appId: "cli_xxx"
      appSecret: secret:feishu-assistant # fractalbot secret set feishu-assistant
      domain: "feishu"                   # 国际版用 "lark"
      allowedUsers:
        - "ou_xxxxx"
```

---

## 多 Agent 架构：agent-manager 详解

> 已通过快速入门跑通单 Agent？本节介绍完整的多 Agent 架构。

FractalBot 通过 `runtime: agentManager` 的 Agent 将消息路由到 [agent-manager](https://github.com/fractalmind-labs/fractalmind-os/tree/main/skills/coordination/agent-manager-skill)（基于 tmux + Claude Code 的 Agent 管理系统）。每个 Agent 跑在独立的 tmux session 里。

### 1. 配置 AGENTS.md

`AGENTS.md` 是 **main Agent**（协调者）的定义文件，放在仓库根目录。它有两部分：

- **YAML frontmatter** — main Agent 的配置
- **Markdown 正文** — 所有 Agent 共享的工作规范

```markdown
---
name: main
description: Main
enabled: true
working_directory: ${REPO_ROOT}
launcher: claude
launcher_args:
  - --dangerously-skip-permissions
heartbeat:
  cron: "*/10 * * * *"
  max_runtime: 8m
  session_mode: auto
  enabled: true
skills:
  - agent-manager
  - use-fractalbot
---

## 安全规则
- 不外泄私有数据
- 破坏性命令需先确认

## Agent 协作
- main 是协调者，分配任务给其他 Agent
- 开发任务 → dev Agent
- 测试审查 → qa Agent
```

| 关键字段 | 说明 |
|----------|------|
| `name: main` | 固定为 `main`；FractalBot 里 agentManager Agent 的 `session` 默认就是 `main` |
| `heartbeat.cron` | 定期巡检的 cron 表达式 |
| `skills` | 必须包含 `agent-manager` + `use-fractalbot` |

#### 让 Claude Code 加载 AGENTS.md

Claude Code 默认只加载 `CLAUDE.md`。配置 SessionStart hook 让它自动读取 `AGENTS.md`：

```bash
# 创建 hook 脚本
mkdir -p .claude/hooks
cat > .claude/hooks/load-agents.sh << 'EOF'
#!/bin/bash
[ -f "$CLAUDE_PROJECT_DIR/AGENTS.md" ] && cat "$CLAUDE_PROJECT_DIR/AGENTS.md"
EOF
chmod +x .claude/hooks/load-agents.sh
```

在 `.claude/settings.json` 中注册：

```json
{
  "hooks": {
    "SessionStart": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "$CLAUDE_PROJECT_DIR/.claude/hooks/load-agents.sh"
          }
        ]
      }
    ]
  }
}
```

> 验证：启动 Claude Code 后问「你的 name 是什么？」，应回答 `main`。

### 2. 创建子 Agent

Agent 定义在 `agents/EMP_*.md` 文件中：

```markdown
---
name: dev
role: developer
description: "dev — 开发 Agent，负责编码和 bug 修复"
working_directory: ${REPO_ROOT}
launcher: claude
launcher_args:
  - --dangerously-skip-permissions
skills:
  - agent-manager
  - use-fractalbot
---

# DEV AGENT
- 实现功能开发和 bug 修复
- 优先通过 PR 提交，方便回滚
```

**frontmatter 字段说明：**

| 字段 | 必填 | 说明 |
|------|------|------|
| `name` | 是 | Agent 标识符，FractalBot 路由时使用此名称 |
| `working_directory` | 是 | 工作目录，`${REPO_ROOT}` 替换为仓库根目录 |
| `launcher` | 是 | 启动器：`claude`、`codex`（OpenAI）、或完整路径 |
| `launcher_args` | 否 | 启动器参数列表 |
| `skills` | 否 | 注入的 skill 名称列表（建议包含 `use-fractalbot`） |
| `enabled` | 否 | 是否可启动，默认 `true` |

你可以创建多个 Agent，分别负责不同职责：

```
agents/
├── EMP_0001.md    # dev — 开发
├── EMP_0002.md    # qa — 测试审查
└── EMP_0003.md    # sre — 运维部署
```

### 3. 配置路由

每个 agent-manager Agent 在 `config.yaml` 里写成一个 Agent，绑定它要接收的渠道（写成 `类型.实例名`）：

```yaml
agents:
  dev:
    workspace: /path/to/your/workspace       # 包含 agents/ 目录的仓库路径
    runtime: agentManager
    session: dev                             # 对应 agents/EMP_*.md 的 name
    channels: [slack.main]
  qa:
    workspace: /path/to/your/workspace
    runtime: agentManager
    session: qa
    channels: [slack.main]

channels:
  slack:
    main:
      # …
      default: dev                           # 两个 Agent 共用时，普通消息给 dev

runtimes:
  agentManager:
    assignTimeoutSeconds: 90
```

一个渠道绑定多个 Agent 时，每条消息按以下顺序选出接收者：

1. 消息开头写 `@qa` 或 `/agent qa`：只对这一条生效；
2. 渠道 `chats` 里固定给某个 Agent 的对话；
3. 发过 `/use qa` 后，这个对话都交给 qa，`/use` 不带名字恢复默认；
4. 渠道 `default`。

`/who` 查看当前给谁。没有绑定任何 Agent 的渠道，`config check` 会报错。详见 [Routing](routing.md)。

### 4. 验证 Agent

```bash
# 列出所有 Agent
python3 .claude/skills/agent-manager/scripts/main.py list

# 启动 Agent
python3 .claude/skills/agent-manager/scripts/main.py start dev

# 分配任务
python3 .claude/skills/agent-manager/scripts/main.py assign dev <<EOF
说 hello world
EOF

# 查看输出
python3 .claude/skills/agent-manager/scripts/main.py monitor dev
```

Agent 运行在 tmux session `agent-dev` 中，可用 `tmux attach -t agent-dev` 直接查看。

---

## 启动与验证

### 启动

```bash
# 前台运行（开发调试）
fractalbot --config ./config.yaml

# 带详细日志
fractalbot --config ./config.yaml --verbose

# 在 tmux 中后台运行
tmux new-session -d -s fractalbot 'fractalbot --config config.yaml'
```

### 端到端验证

| 步骤 | 发送 | 预期结果 |
|------|------|----------|
| 1 | `/whoami` | 回复 User ID（示例：`U08XXXXXX`） |
| 2 | `/ping` | 回复 `pong` |
| 3 | `/agents` | 列出可用 Agent（示例：`dev`） |
| 4 | `Hello` | Agent 接收任务并回复（示例含 `[dev]`） |
| 5 | `/agent dev 写个 hello world` | 指定 Agent 执行任务 |

### HTTP 健康检查

```bash
curl -s http://127.0.0.1:18789/health          # 返回 OK
curl -s http://127.0.0.1:18789/status | python3 -m json.tool
```

**成功标准：** 启动无报错 + `/whoami` 返回 ID + 普通消息被路由到 Agent + `/health` 返回 OK。

---

## 常用命令速查

| 命令 | 说明 | 权限 |
|------|------|------|
| `/whoami` | 查看你的用户 ID | 所有人 |
| `/ping` | 健康检查 | 所有人 |
| `/agents` | 列出可用 Agent | 所有人 |
| `/agent <name> <task>` | 指定 Agent 执行任务 | 所有人 |
| `@<name> <text>` | 这条消息交给指定 Agent（共享渠道） | 所有人 |
| `/use [name]` | 之后这个对话都交给该 Agent；不带名字恢复默认 | 所有人 |
| `/who` | 查看当前对话交给谁、可选哪些 Agent | 所有人 |
| `/monitor <name> [lines]` | 查看 Agent 最近输出（最多 200 行） | 所有人 |
| `/startagent <name>` | 启动指定 Agent | 仅 Admin |
| `/stopagent <name>` | 停止指定 Agent | 仅 Admin |
| `/doctor` | 运行诊断检查 | 仅 Admin |
| `tool echo <text>` | 内置 tool runtime 回声测试 | 所有人 |

> 普通消息交给渠道的默认 Agent；只绑定了一个 Agent 的渠道，默认就是它。`/monitor`、`/startagent`、`/stopagent` 只对 agentManager 运行时的 Agent 有效。

---

## FAQ

### Q: 连接报 `connection refused`

网关未运行或端口不对。

```bash
ps aux | grep fractalbot     # 检查进程
lsof -i :18789               # 检查端口
grep -A2 'gateway:' config.yaml  # 确认配置
```

### Q: Telegram 报 `unauthorized`

botToken 无效或已过期。回到 @BotFather → `/mybots` 确认 token，如已泄露点击 Revoke Token 重新生成。

### Q: 发消息后 Agent 超时无响应

agent-manager 未运行或 Agent 会话不存在。

```bash
tmux ls                      # 检查活跃会话
python3 .claude/skills/agent-manager/scripts/main.py start dev  # 手动启动
tmux attach -t agent-dev     # 查看 Agent 日志
```

也可通过 Bot 命令诊断：`/doctor` 或 `/monitor dev`。

### Q: Slack 报 `channel "slack" not found`

Slack 渠道没有启动。运行 `fractalbot config check`，确认三件事：`channels.slack.main` 已配置；它绑定到了某个 Agent；`slack-bot`、`slack-app` 两个密钥显示 set。`/status` 里 `channels[].unavailable` 会写明未启动的原因。

### Q: Slack 报 `missing_scope` / `not_authed`

通常是权限不足或 token 不匹配：

1. 检查 `botToken` 是否为 `xoxb-`、`appToken` 是否为 `xapp-`
2. 确认 Bot Scopes 至少包含：`chat:write`、`im:history`、`im:read`
3. 重新安装 App：**Reinstall to Workspace**

### Q: Slack 一直连不上（Socket Mode not enabled / connection failed）

1. 在 Slack App 设置确认 **Socket Mode = Enable**
2. 确认 App-Level Token 有 `connections:write`
3. 修改后重新启动 FractalBot，再测 `/ping`

### Q: 飞书/Discord 配置后没反应

1. 运行 `fractalbot config check`，确认渠道已绑定 Agent、密钥已设置
2. 确认 `allowedUsers` 中包含你的用户 ID（用 `/whoami` 查询）
3. 确认 Bot 已邀请到工作区/服务器
4. 查看 FractalBot 终端日志排查

---

## 附录：从 OpenClaw 迁移

> 仅限从 [OpenClaw/Clawdbot](https://github.com/clawdbot/clawdbot)（Node.js）迁移的用户。新用户跳过本节。

| 对比项 | OpenClaw (Clawdbot) | FractalBot |
|--------|---------------------|------------|
| 语言 | Node.js / TypeScript | Go |
| 安装 | `npm install` | `go build` 或一键脚本 |
| 配置格式 | `.env` + JSON | `config.yaml` |
| Telegram 模式 | Webhook | Polling（默认）/ Webhook |
| Agent 管理 | 内置 | 外部 agent-manager (tmux) |
| 渠道支持 | Telegram / Slack | Telegram / Slack / Discord / 飞书 |
| 多 Agent | 单 Agent | 多 Agent 并发 |

迁移步骤：

1. 安装 FractalBot（见上方「安装」）
2. 把 `.env` 中的 token 搬到 `config.yaml`（如 `TELEGRAM_BOT_TOKEN` → `channels.telegram.botToken`）
3. 安装 skills：`npx openskills install agent-manager && npx openskills install use-fractalbot`
4. 启动并测试：`fractalbot --config config.yaml`，发送 `/ping` 和 `/whoami` 验证
5. 确认正常后停用旧服务

---

## 更多资源

- [GitHub 仓库](https://github.com/fractalmind-labs/fractalmind-os/tree/main/runtime/fractalbot)
- [config.example.yaml](https://github.com/fractalmind-labs/fractalmind-os/blob/main/runtime/fractalbot/config.example.yaml) — 完整配置参考
- [agent-manager skill](https://github.com/fractalmind-labs/fractalmind-os/tree/main/skills/coordination/agent-manager-skill) — Agent 生命周期管理
- [use-fractalbot skill](https://github.com/fractalmind-labs/fractalmind-os/tree/main/skills/interfaces/use-fractalbot-skill) — Agent 回复消息的 skill
- [ROADMAP.md](https://github.com/fractalmind-labs/fractalmind-os/blob/main/runtime/fractalbot/ROADMAP.md) — 开发路线图
- [CONTRIBUTING.md](https://github.com/fractalmind-labs/fractalmind-os/blob/main/runtime/fractalbot/CONTRIBUTING.md) — 贡献指南
