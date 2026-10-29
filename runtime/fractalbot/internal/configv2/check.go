package configv2

import (
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/fractalmind-labs/fractalmind-os/runtime/fractalbot/internal/secrets"
)

// SecretStatus is one secret's resolution without its value.
type SecretStatus struct {
	Name   string
	Store  string
	Status string // set, missing or the read error
	UsedBy []string
}

// Report is what `fractalbot config check` prints.
type Report struct {
	Config       *Config
	DefaultStore string
	StoreReason  string
	Secrets      []SecretStatus
	// Problems fail the check; the configuration itself already validated.
	Problems []string
}

// OK reports whether the gateway can start with every channel working.
func (r Report) OK() bool { return len(r.Problems) == 0 }

// CheckOptions are the machine facts a check reads.
type CheckOptions struct {
	Secrets  secrets.Environment
	LookPath func(string) (string, error)
}

// HostCheckOptions checks against the running machine.
func HostCheckOptions() CheckOptions {
	return CheckOptions{Secrets: secrets.HostEnvironment(), LookPath: exec.LookPath}
}

// Check resolves every secret and checks the tools the agents' runtimes run.
// It reports whether each secret is set, never its value.
func Check(cfg *Config, opts CheckOptions) Report {
	rep := Report{Config: cfg}
	resolver, err := secrets.NewResolver(cfg.Secrets, cfg.DataDir, opts.Secrets)
	if err != nil {
		rep.Problems = append(rep.Problems, err.Error())
		return rep
	}
	store, reason := resolver.Default()
	rep.DefaultStore, rep.StoreReason = store.Kind(), reason
	usedBy := map[string][]string{}
	for _, inst := range cfg.ChannelList() {
		for _, s := range inst.Secrets {
			usedBy[s] = append(usedBy[s], inst.Ref)
		}
	}
	for _, res := range resolver.Resolve(cfg.SecretNames()) {
		st := SecretStatus{Name: res.Name, Store: res.Store, Status: "set", UsedBy: usedBy[res.Name]}
		switch {
		case res.Missing():
			st.Status = "missing"
			rep.Problems = append(rep.Problems, fmt.Sprintf("secret %s is not set in the %s store (run: fractalbot secret set %s)", res.Name, res.Store, res.Name))
		case res.Err != nil:
			st.Status = "error: " + res.Err.Error()
			rep.Problems = append(rep.Problems, fmt.Sprintf("secret %s cannot be read: %v", res.Name, res.Err))
		}
		rep.Secrets = append(rep.Secrets, st)
	}
	runtimes := map[string]bool{}
	for _, a := range cfg.Agents {
		runtimes[a.Runtime] = true
	}
	if runtimes[RuntimeCodexApp] {
		if _, err := opts.LookPath(cfg.Runtimes.CodexApp.Codex); err != nil {
			rep.Problems = append(rep.Problems, fmt.Sprintf("runtimes.codexApp.codex: %q was not found", cfg.Runtimes.CodexApp.Codex))
		}
	}
	if runtimes[RuntimeAgentManager] {
		if _, err := opts.LookPath("tmux"); err != nil {
			rep.Problems = append(rep.Problems, "agentManager agents need tmux, which was not found")
		}
	}
	return rep
}

// Write prints the report.
func (r Report) Write(w io.Writer) {
	cfg := r.Config
	paths := cfg.Paths()
	fmt.Fprintf(w, "Configuration: %s\n", cfg.Path)
	fmt.Fprintf(w, "Data directory: %s\n", paths.Root())
	fmt.Fprintf(w, "Gateway: %s:%d\n", cfg.Gateway.Bind, cfg.Gateway.Port)

	fmt.Fprintln(w, "\nChannels:")
	for _, inst := range cfg.ChannelList() {
		agents := cfg.AgentsFor(inst.Ref)
		line := "  " + inst.Ref + " -> " + strings.Join(agents, ", ")
		if len(agents) > 1 {
			line += " (shared, default " + inst.Sharing.Default + ")"
		}
		fmt.Fprintln(w, line)
	}

	fmt.Fprintln(w, "\nAgents:")
	for _, name := range sortedKeys(cfg.Agents) {
		a := cfg.Agents[name]
		fmt.Fprintf(w, "  %s: %s, workspace %s\n", name, a.Runtime, a.Workspace)
		switch a.Runtime {
		case RuntimeClaudeDesktop:
			fmt.Fprintf(w, "    inbox %s\n", paths.Inbox(name))
		case RuntimeCodexApp:
			fmt.Fprintf(w, "    Codex thread %s\n", a.Session)
		case RuntimeDeepSeekHarness:
			fmt.Fprintf(w, "    DeepSeek Harness CDP %s\n", cfg.Runtimes.DeepSeekHarness.CDPEndpoint)
		case RuntimeAgentManager:
			fmt.Fprintf(w, "    agent-manager agent %s via %s\n", a.Session, cfg.AgentManagerScript(a))
		}
		for _, id := range sortedKeys(cfg.Schedules.Jobs) {
			j := cfg.Schedules.Jobs[id]
			if j.Agent == name {
				fmt.Fprintf(w, "    schedule %s: %s (%s)\n", id, j.Cron, cfg.JobTimezone(j))
			}
		}
	}

	fmt.Fprintf(w, "\nSecrets (default store: %s, %s):\n", r.DefaultStore, r.StoreReason)
	for _, s := range r.Secrets {
		fmt.Fprintf(w, "  %s [%s]: %s, used by %s\n", s.Name, s.Store, s.Status, strings.Join(s.UsedBy, ", "))
	}

	fmt.Fprintln(w, "\nState files:")
	fmt.Fprintf(w, "  schedules %s\n  selections %s\n", paths.Schedules(), paths.Selections())
	for _, inst := range cfg.ChannelList() {
		fmt.Fprintf(w, "  %s %s\n", inst.Ref, paths.ChannelState(inst.Type, inst.Name))
	}

	if r.OK() {
		fmt.Fprintln(w, "\nOK")
		return
	}
	fmt.Fprintln(w, "\nProblems:")
	for _, p := range r.Problems {
		fmt.Fprintln(w, "  - "+p)
	}
}
