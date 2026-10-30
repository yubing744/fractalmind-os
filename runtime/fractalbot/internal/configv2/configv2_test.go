package configv2

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fractalmind-labs/fractalmind-os/runtime/fractalbot/internal/secrets"
)

const threadID = "01a15669-badd-7c31-b094-660961b49af7"

type fixture struct {
	home, xiaoyi, burry string
	platform            Platform
}

func newFixture(t *testing.T, goos string) fixture {
	t.Helper()
	home := t.TempDir()
	f := fixture{home: home, xiaoyi: filepath.Join(home, "xiaoyi-clawd"), burry: filepath.Join(home, "burry-claw")}
	for _, dir := range []string{f.xiaoyi, filepath.Join(f.burry, "tasks")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.burry, "tasks", "radar.md"), []byte("check BTC"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.platform = HostPlatform()
	f.platform.GOOS = goos
	f.platform.Home = home
	f.platform.Getenv = func(string) string { return "" }
	return f
}

// localConfig mirrors this machine's intended V2 configuration.
const localConfig = `version: 2
dataDir: ~/.fractalbot
timezone: Asia/Shanghai
channels:
  feishu:
    xiaoyi:
      appId: cli_xiaoyi
      appSecret: secret:feishu-xiaoyi
      allowedUsers: [ou_owner]
    burry:
      appId: cli_burry
      appSecret: secret:feishu-burry
  telegram:
    main:
      botToken: secret:telegram-main
      allowedUsers: [5088760910]
      default: xiaoyi
  imessage:
    main:
      recipient: "+8600000000000"
      default: xiaoyi
agents:
  xiaoyi:
    workspace: ~/xiaoyi-clawd
    runtime: codexApp
    session: ` + threadID + `
    channels: [feishu.xiaoyi, telegram.main, imessage.main]
  xiaoyi-desktop:
    workspace: ~/xiaoyi-clawd
    runtime: claudeDesktop
  burry:
    workspace: ~/burry-claw
    runtime: agentManager
    channels: [feishu.burry, telegram.main, imessage.main]
schedules:
  maxConcurrent: 3
  jobs:
    xiaoyi-heartbeat:
      agent: xiaoyi-desktop
      cron: "11 * * * *"
      backoff: {idle: "11 */3 * * *", deep-idle: "11 9 * * *"}
      resetOnInbound: true
      prompt: Read HEARTBEAT.md and follow it.
    burry-btc-risk-radar:
      agent: burry
      cron: "5 */2 * * *"
      promptFile: tasks/radar.md
`

func TestParseLocalConfig(t *testing.T) {
	f := newFixture(t, "darwin")
	cfg, err := Parse([]byte(localConfig), f.platform)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.DataDir != filepath.Join(f.home, ".fractalbot") || cfg.Gateway.Bind != "127.0.0.1" || cfg.Gateway.Port != 18789 {
		t.Fatalf("defaults: %+v %+v", cfg.DataDir, cfg.Gateway)
	}
	if got := cfg.Agents["burry"]; got.Session != "main" || got.Workspace != f.burry {
		t.Fatalf("burry agent: %+v", got)
	}
	if got := cfg.AgentManagerScript(cfg.Agents["burry"]); got != filepath.Join(f.burry, ".claude/skills/agent-manager/scripts/main.py") {
		t.Fatalf("script: %s", got)
	}
	if got := strings.Join(cfg.AgentsFor("telegram.main"), ","); got != "burry,xiaoyi" {
		t.Fatalf("telegram.main agents: %s", got)
	}
	if got := strings.Join(cfg.SecretNames(), ","); got != "feishu-burry,feishu-xiaoyi,telegram-main" {
		t.Fatalf("secret names: %s", got)
	}
	paths := cfg.Paths()
	if paths.Inbox("xiaoyi-desktop") != filepath.Join(f.home, ".fractalbot", "inbox", "xiaoyi-desktop") ||
		paths.ChannelState("feishu", "xiaoyi") != filepath.Join(f.home, ".fractalbot", "state", "channels", "feishu-xiaoyi.json") {
		t.Fatalf("derived paths: %s %s", paths.Inbox("xiaoyi-desktop"), paths.ChannelState("feishu", "xiaoyi"))
	}
}

func TestRejectsVersion1WithRewrites(t *testing.T) {
	v1 := `agents:
  router: ohMyCode
  ohMyCode:
    enabled: true
    targets:
      burry: {receivers: [{channel: feishu, receiverId: cli_x}]}
  heartbeat:
    jobs: [{id: a, runtime: claudeDesktop, agent: main, text: hi}]
channels:
  feishu:
    enabled: true
    bots: {burry: {appId: x, appSecret: plaintext-value}}
`
	_, err := Parse([]byte(v1), newFixture(t, "darwin").platform)
	if err == nil {
		t.Fatal("expected a version 1 error")
	}
	for _, want := range []string{"version 1", "agents.router was removed", "channels.feishu.bots.<name> is now", "schedules.jobs", "text is now prompt"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing %q in:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "plaintext-value") {
		t.Fatalf("error echoes a secret: %v", err)
	}
	if _, err := Parse([]byte("version: 1\n"), newFixture(t, "darwin").platform); err == nil || !strings.Contains(err.Error(), "unsupported version") {
		t.Fatalf("version 1: %v", err)
	}
}

func TestUnknownFieldAndPlaintextSecret(t *testing.T) {
	f := newFixture(t, "darwin")
	withText := strings.Replace(localConfig, "prompt: Read HEARTBEAT.md and follow it.", "text: Read HEARTBEAT.md", 1)
	if _, err := Parse([]byte(withText), f.platform); err == nil || !strings.Contains(err.Error(), `unknown field "text" (text is now prompt`) {
		t.Fatalf("unknown field: %v", err)
	}
	plain := strings.Replace(localConfig, "secret:telegram-main", "1234:plaintext-token-value", 1)
	_, err := Parse([]byte(plain), f.platform)
	if err == nil || !strings.Contains(err.Error(), "secret reference") || strings.Contains(err.Error(), "plaintext-token-value") {
		t.Fatalf("plaintext secret: %v", err)
	}
}

func TestValidationProblems(t *testing.T) {
	cases := []struct {
		name, from, to, want string
	}{
		{"unbound channel", "channels: [feishu.burry, telegram.main, imessage.main]", "channels: [telegram.main, imessage.main]", "channels.feishu.burry: no agent receives this channel"},
		{"shared without default", "      default: xiaoyi\nagents:", "agents:", "channels.imessage.main.default: required because burry, xiaoyi share"},
		{"default not bound", "      allowedUsers: [5088760910]\n      default: xiaoyi", "      allowedUsers: [5088760910]\n      default: xiaoyi-desktop", "channels.telegram.main.default: xiaoyi-desktop is not bound"},
		{"unknown channel ref", "[feishu.xiaoyi, telegram.main", "[feishu.xioayi, feishu.xiaoyi, telegram.main", "feishu.xioayi is not configured"},
		{"codex session", "session: " + threadID, "session: main", "need the Codex thread ID"},
		{"missing workspace", "~/burry-claw", "~/nowhere", "agents.burry.workspace"},
		{"relative workspace", "~/burry-claw", "burry-claw", "must be an absolute path"},
		{"both prompts", "promptFile: tasks/radar.md", "promptFile: tasks/radar.md\n      prompt: x", "set exactly one of prompt and promptFile"},
		{"prompt escapes", "promptFile: tasks/radar.md", "promptFile: ../secret.md", "must be a path inside the agent's workspace"},
		{"prompt missing", "promptFile: tasks/radar.md", "promptFile: tasks/none.md", "does not exist"},
		{"heartbeatRun runtime", "      resetOnInbound: true\n", "      resetOnInbound: true\n      mode: heartbeatRun\n", "heartbeatRun needs an agentManager agent"},
		{"bad cron", `cron: "5 */2 * * *"`, `cron: "61 * * * *"`, "schedules.jobs.burry-btc-risk-radar.cron"},
		{"unknown job agent", "agent: burry\n", "agent: bury\n", `"bury" is not a configured agent`},
	}
	f := newFixture(t, "darwin")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(localConfig, tc.from) {
				t.Fatalf("fixture lacks %q", tc.from)
			}
			_, err := Parse([]byte(strings.Replace(localConfig, tc.from, tc.to, 1)), f.platform)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestPlatformAvailability(t *testing.T) {
	f := newFixture(t, "linux")
	_, err := Parse([]byte(localConfig), f.platform)
	if err == nil {
		t.Fatal("expected Linux problems")
	}
	for _, want := range []string{"iMessage is only available on macOS", "codexApp needs the desktop app", "claudeDesktop needs the desktop app"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing %q in %v", want, err)
		}
	}
	f = newFixture(t, "windows")
	f.platform.ExpandPath("C:/x") // compile check of the Windows branch below
	if got, err := f.platform.ExpandPath("C:/Users/me/bot"); err != nil || got != `C:\Users\me\bot` {
		t.Fatalf("windows path: %q %v", got, err)
	}
	if _, err := f.platform.ExpandPath("relative/bot"); err == nil {
		t.Fatal("windows relative path accepted")
	}
}

func TestDeepSeekHarnessRuntime(t *testing.T) {
	f := newFixture(t, "darwin")
	withHarness := strings.Replace(localConfig, "agents:\n", `runtimes:
  deepseekHarness:
    cdpEndpoint: http://127.0.0.1:9224
agents:
  harness:
    workspace: ~/xiaoyi-clawd
    runtime: deepseekHarness
    session: session-12345678-1234-1234-1234-123456789abc
`, 1)
	cfg, err := Parse([]byte(withHarness), f.platform)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Agents["harness"].Runtime; got != RuntimeDeepSeekHarness {
		t.Fatalf("runtime: %q", got)
	}

	invalidEndpoint := strings.Replace(withHarness, "http://127.0.0.1:9224", "http://192.168.1.2:9224", 1)
	if _, err := Parse([]byte(invalidEndpoint), f.platform); err == nil || !strings.Contains(err.Error(), "must use localhost") {
		t.Fatalf("non-loopback endpoint: %v", err)
	}
	invalidSession := strings.Replace(withHarness, "session: session-12345678-1234-1234-1234-123456789abc", "session: main", 1)
	if _, err := Parse([]byte(invalidSession), f.platform); err == nil || !strings.Contains(err.Error(), "need the DeepSeek Harness session ID") {
		t.Fatalf("invalid session: %v", err)
	}
}

func TestDefaultDirectories(t *testing.T) {
	env := map[string]string{"XDG_STATE_HOME": "/x/state", "LOCALAPPDATA": `C:\Users\me\AppData\Local`, "APPDATA": `C:\Users\me\AppData\Roaming`}
	for _, tc := range []struct{ goos, config, data string }{
		{"darwin", "/home/me/.config/fractalbot/config.yaml", "/home/me/Library/Application Support/FractalBot"},
		{"linux", "/home/me/.config/fractalbot/config.yaml", "/x/state/fractalbot"},
		{"windows", `C:\Users\me\AppData\Roaming\FractalBot\config.yaml`, `C:\Users\me\AppData\Local\FractalBot`},
	} {
		p := Platform{GOOS: tc.goos, Home: "/home/me", Getenv: func(k string) string { return env[k] }}
		if p.DefaultConfigPath() != tc.config || p.DefaultDataDir() != tc.data {
			t.Fatalf("%s: %s %s", tc.goos, p.DefaultConfigPath(), p.DefaultDataDir())
		}
	}
}

func TestCheckReportsSecretsWithoutValues(t *testing.T) {
	f := newFixture(t, "darwin")
	cfg, err := Parse([]byte(strings.Replace(localConfig, "timezone: Asia/Shanghai", "timezone: Asia/Shanghai\nsecrets: {store: env}", 1)), f.platform)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Path = "config.yaml"
	vars := map[string]string{
		secrets.EnvVar("feishu-xiaoyi"): "value-xiaoyi-123",
		secrets.EnvVar("feishu-burry"):  "value-burry-123",
	}
	opts := CheckOptions{
		Secrets: secrets.Environment{GOOS: "darwin", LookupEnv: func(k string) (string, bool) { v, ok := vars[k]; return v, ok }, Exists: func(string) bool { return false }},
		LookPath: func(name string) (string, error) {
			if name == "codex" {
				return "", errors.New("not found")
			}
			return "/usr/bin/" + name, nil
		},
	}
	rep := Check(cfg, opts)
	var out bytes.Buffer
	rep.Write(&out)
	text := out.String()
	if rep.OK() || !strings.Contains(text, "telegram-main [env]: missing, used by telegram.main") ||
		!strings.Contains(text, `runtimes.codexApp.codex: "codex" was not found`) ||
		!strings.Contains(text, "telegram.main -> burry, xiaoyi (shared, default xiaoyi)") {
		t.Fatalf("report:\n%s", text)
	}
	if strings.Contains(text, "value-xiaoyi-123") {
		t.Fatalf("report prints a secret value:\n%s", text)
	}
	vars[secrets.EnvVar("telegram-main")] = "value-telegram-123"
	opts.LookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	if rep := Check(cfg, opts); !rep.OK() {
		t.Fatalf("expected OK, got %v", rep.Problems)
	}
}

func TestExampleConfigParses(t *testing.T) {
	data, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	p := Platform{GOOS: "darwin", Home: "/home/me", Getenv: func(string) string { return "" },
		DirExists: func(string) bool { return true }, FileExists: func(string) bool { return true }}
	cfg, err := Parse(data, p)
	if err != nil {
		t.Fatalf("config.example.yaml: %v", err)
	}
	if cfg.DataDir != "/home/me/.fractalbot" || len(cfg.Schedules.Jobs) != 2 {
		t.Fatalf("example: %+v", cfg)
	}
}

func TestDocExampleParses(t *testing.T) {
	data, err := os.ReadFile("../../docs/config-v2.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	_, after, ok := strings.Cut(doc, "## Example\n\n```yaml\n")
	if !ok {
		t.Fatal("docs/config-v2.md has no Example section")
	}
	example, _, _ := strings.Cut(after, "```")
	p := Platform{GOOS: "darwin", Home: "/home/me", Getenv: func(string) string { return "" },
		DirExists: func(string) bool { return true }, FileExists: func(string) bool { return true }}
	cfg, err := Parse([]byte(example), p)
	if err != nil {
		t.Fatalf("docs/config-v2.md example: %v", err)
	}
	if len(cfg.Agents) != 3 || len(cfg.Schedules.Jobs) != 2 {
		t.Fatalf("example: %d agents, %d jobs", len(cfg.Agents), len(cfg.Schedules.Jobs))
	}
}

func TestReadmeExampleParses(t *testing.T) {
	data, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	_, after, ok := strings.Cut(string(data), "```yaml\nversion: 2\n")
	if !ok {
		t.Fatal("README.md has no configuration example")
	}
	example, _, _ := strings.Cut(after, "```")
	p := Platform{GOOS: "darwin", Home: "/home/me", Getenv: func(string) string { return "" },
		DirExists: func(string) bool { return true }, FileExists: func(string) bool { return true }}
	if _, err := Parse([]byte("version: 2\n"+example), p); err != nil {
		t.Fatalf("README.md example: %v", err)
	}
}
