package configv2

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Platform is the operating system facts the configuration depends on.
// Tests replace it.
type Platform struct {
	GOOS   string
	Home   string
	Getenv func(string) string
	// DirExists and FileExists check workspaces and prompt files.
	DirExists  func(string) bool
	FileExists func(string) bool
}

// HostPlatform describes the running process.
func HostPlatform() Platform {
	home, _ := os.UserHomeDir()
	return Platform{
		GOOS:   runtime.GOOS,
		Home:   home,
		Getenv: os.Getenv,
		DirExists: func(p string) bool {
			info, err := os.Stat(p)
			return err == nil && info.IsDir()
		},
		FileExists: func(p string) bool {
			info, err := os.Stat(p)
			return err == nil && info.Mode().IsRegular()
		},
	}
}

func (p Platform) join(elem ...string) string {
	if p.GOOS == "windows" {
		return strings.Join(elem, `\`)
	}
	return strings.Join(elem, "/")
}

func (p Platform) envOr(key, fallback string) string {
	if v := p.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// DefaultConfigPath is where the configuration lives when --config is not
// given: ~/.config/fractalbot/config.yaml on macOS, the XDG config directory
// on Linux and %APPDATA%\FractalBot on Windows.
func (p Platform) DefaultConfigPath() string {
	switch p.GOOS {
	case "windows":
		return p.join(p.envOr("APPDATA", p.join(p.Home, "AppData", "Roaming")), "FractalBot", "config.yaml")
	case "linux":
		return p.join(p.envOr("XDG_CONFIG_HOME", p.join(p.Home, ".config")), "fractalbot", "config.yaml")
	default:
		return p.join(p.Home, ".config", "fractalbot", "config.yaml")
	}
}

// DefaultDataDir is where runtime data lives when dataDir is not set.
func (p Platform) DefaultDataDir() string {
	switch p.GOOS {
	case "windows":
		return p.join(p.envOr("LOCALAPPDATA", p.join(p.Home, "AppData", "Local")), "FractalBot")
	case "linux":
		return p.join(p.envOr("XDG_STATE_HOME", p.join(p.Home, ".local", "state")), "fractalbot")
	default:
		return p.join(p.Home, "Library", "Application Support", "FractalBot")
	}
}

// ExpandPath resolves `~` and requires an absolute result. Windows accepts
// forward slashes.
func (p Platform) ExpandPath(raw string) (string, error) {
	path := strings.TrimSpace(raw)
	if path == "" {
		return "", errors.New("is empty")
	}
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		if p.Home == "" {
			return "", errors.New("uses ~ but the home directory is unknown")
		}
		path = p.Home + path[1:]
	}
	if p.GOOS == "windows" {
		path = strings.ReplaceAll(path, "/", `\`)
		if len(path) < 3 || path[1] != ':' || path[2] != '\\' {
			if !strings.HasPrefix(path, `\\`) {
				return "", fmt.Errorf("%q must be an absolute path", raw)
			}
		}
		return path, nil
	}
	if !strings.HasPrefix(path, "/") {
		return "", fmt.Errorf("%q must be an absolute path", raw)
	}
	return filepath.Clean(path), nil
}

// support says where channels and runtimes work. An empty reason means
// supported.
func (p Platform) channelSupport(kind string) string {
	if kind == ChannelIMessage && p.GOOS != "darwin" {
		return "iMessage is only available on macOS"
	}
	return ""
}

func (p Platform) runtimeSupport(name string) string {
	switch name {
	case RuntimeClaudeDesktop, RuntimeCodexApp, RuntimeDeepSeekHarness:
		if p.GOOS != "darwin" && p.GOOS != "windows" {
			return name + " needs the desktop app, which runs on macOS and Windows"
		}
	case RuntimeAgentManager:
		if p.GOOS == "windows" {
			return "agentManager needs tmux; run FractalBot inside WSL on Windows"
		}
	}
	return ""
}
