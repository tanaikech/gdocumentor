package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// SetupAgentResult records the installation/uninstallation/status result for a single AI agent.
type SetupAgentResult struct {
	Agent          string   `json:"agent"`
	Scope          string   `json:"scope"`
	Action         string   `json:"action"` // "installed", "uninstalled", "status"
	MCPInstalled   bool     `json:"mcpInstalled"`
	SkillInstalled bool     `json:"skillInstalled"`
	MCPConfigPath  string   `json:"mcpConfigPath,omitempty"`
	Command        string   `json:"command,omitempty"`
	Args           []string `json:"args,omitempty"`
	SkillPaths     []string `json:"skillPaths,omitempty"`
	RulePaths      []string `json:"rulePaths,omitempty"`
	AgentPaths     []string `json:"agentPaths,omitempty"`
	WSLHostBridge  bool     `json:"wslHostBridge,omitempty"`
	Notes          string   `json:"notes,omitempty"`
}

// SetupReport is the structured output of `gdocumentor setup`.
type SetupReport struct {
	Action     string             `json:"action"` // "install", "uninstall", "status"
	TargetOS   string             `json:"targetOs"`
	WSLHost    bool               `json:"wslHost"`
	WSLDistro  string             `json:"wslDistro,omitempty"`
	WinUser    string             `json:"winUser,omitempty"`
	BinaryPath string             `json:"binaryPath"`
	Scope      string             `json:"scope"`
	Results    []SetupAgentResult `json:"results"`
}

var supportedAgentsOrder = []string{
	"antigravity",
	"claude-desktop",
	"claude-code",
	"cursor",
	"vscode",
	"windsurf",
	"gemini-cli",
}

// NormalizeAgentName maps user-friendly agent aliases to canonical agent identifiers.
func NormalizeAgentName(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "all", "*":
		return "all", nil
	case "antigravity", "agy", "antigravity-cli", "antigravity-ide":
		return "antigravity", nil
	case "claude-desktop", "claudedesktop", "claude-app", "claude_desktop":
		return "claude-desktop", nil
	case "claude-code", "claudecode", "claude", "claude-cli", "claude_code":
		return "claude-code", nil
	case "cursor", "cursor-ide", "cursor-cli":
		return "cursor", nil
	case "vscode", "vs-code", "code", "copilot", "github-copilot", "gh-copilot":
		return "vscode", nil
	case "windsurf", "codeium", "windsurf-ide":
		return "windsurf", nil
	case "gemini-cli", "gemini", "geminicli", "gemini_cli":
		return "gemini-cli", nil
	default:
		return "", fmt.Errorf("unsupported --agent %q (supported: all, antigravity, claude-desktop, claude-code, cursor, vscode, windsurf, gemini-cli)", raw)
	}
}

func parseTargetAgents(spec string) ([]string, error) {
	trimmed := strings.TrimSpace(spec)
	if trimmed == "" || strings.EqualFold(trimmed, "all") {
		out := make([]string, len(supportedAgentsOrder))
		copy(out, supportedAgentsOrder)
		return out, nil
	}
	parts := strings.Split(trimmed, ",")
	seen := make(map[string]bool)
	var out []string
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			continue
		}
		canon, err := NormalizeAgentName(p)
		if err != nil {
			return nil, err
		}
		if canon == "all" {
			allCopy := make([]string, len(supportedAgentsOrder))
			copy(allCopy, supportedAgentsOrder)
			return allCopy, nil
		}
		if !seen[canon] {
			seen[canon] = true
			out = append(out, canon)
		}
	}
	if len(out) == 0 {
		out = make([]string, len(supportedAgentsOrder))
		copy(out, supportedAgentsOrder)
	}
	return out, nil
}

// ExecuteSetup runs the multi-agent MCP server & skill installer/uninstaller/status checker.
func ExecuteSetup(ctx context.Context, opts *Options, stdout, stderr io.Writer) error {
	_ = ctx
	if opts.SetupAll && strings.TrimSpace(opts.AgentTarget) == "" {
		opts.AgentTarget = "all"
	}

	action := "install"
	if opts.SetupStatus {
		action = "status"
	} else if opts.Uninstall {
		action = "uninstall"
	}

	if action == "install" && !opts.SetupAll && strings.TrimSpace(opts.AgentTarget) == "" {
		if !opts.WSLHost && strings.TrimSpace(opts.SetupScope) == "" && strings.TrimSpace(opts.WSLDistro) == "" && strings.TrimSpace(opts.WinUser) == "" && strings.TrimSpace(opts.BinaryPath) == "" && !opts.JSONOutput {
			PrintSubcommandHelp(stdout, "setup")
			return nil
		}
		return emitError(stdout, stderr, opts.JSONOutput, fmt.Errorf("setup install requires --all (-a) to install for all supported AI clients, or --agent <name> for specific client(s) (e.g. 'gdocumentor setup --all', 'gdocumentor setup --all --wsl-host', or 'gdocumentor setup --agent claude-desktop --wsl-host'; run 'gdocumentor setup --help' for usage)"))
	}

	agents, err := parseTargetAgents(opts.AgentTarget)
	if err != nil {
		return emitError(stdout, stderr, opts.JSONOutput, err)
	}

	scope := strings.ToLower(strings.TrimSpace(opts.SetupScope))
	switch scope {
	case "", "user", "global":
		scope = "user"
	case "project", "workspace", "local":
		scope = "project"
	case "both", "all":
		scope = "both"
	default:
		return emitError(stdout, stderr, opts.JSONOutput, fmt.Errorf("invalid --scope %q: must be 'user', 'project', or 'both'", opts.SetupScope))
	}

	targetOS := runtime.GOOS
	if envOS := strings.TrimSpace(os.Getenv("GDOCUMENTOR_SETUP_OS")); envOS != "" {
		targetOS = strings.ToLower(envOS)
	}

	binPath := resolveSetupBinaryPath(opts.BinaryPath, targetOS, opts.WSLHost)
	mcpCmd := binPath
	mcpArgs := []string{"mcp"}

	var winUser string
	var winHomeDir string
	if opts.WSLHost {
		var wslErr error
		winUser, winHomeDir, wslErr = resolveWindowsHostHome(opts.WinUser)
		if wslErr != nil {
			return emitError(stdout, stderr, opts.JSONOutput, wslErr)
		}
		mcpCmd = "wsl.exe"
		if strings.TrimSpace(opts.WSLDistro) != "" {
			mcpArgs = []string{"-d", strings.TrimSpace(opts.WSLDistro), "--exec", binPath, "mcp"}
		} else {
			mcpArgs = []string{"--exec", binPath, "mcp"}
		}
	}

	homeDir := resolveSetupUserHome(targetOS, opts.WSLHost, winHomeDir)
	projectDir := strings.TrimSpace(opts.WorkDir)
	if projectDir == "" {
		if cwd, cwdErr := os.Getwd(); cwdErr == nil {
			projectDir = cwd
		} else {
			projectDir = "."
		}
	}

	assets := loadPluginAssets(projectDir)

	var scopesToRun []string
	switch scope {
	case "user":
		scopesToRun = []string{"user"}
	case "project":
		scopesToRun = []string{"project"}
	case "both":
		scopesToRun = []string{"user", "project"}
	}

	var results []SetupAgentResult
	for _, sc := range scopesToRun {
		for _, ag := range agents {
			res, agErr := processAgentSetup(ag, sc, action, targetOS, opts.WSLHost, homeDir, projectDir, mcpCmd, mcpArgs, assets)
			if agErr != nil {
				return emitError(stdout, stderr, opts.JSONOutput, agErr)
			}
			results = append(results, res)
		}
	}

	report := &SetupReport{
		Action:     action,
		TargetOS:   targetOS,
		WSLHost:    opts.WSLHost,
		WSLDistro:  opts.WSLDistro,
		WinUser:    winUser,
		BinaryPath: binPath,
		Scope:      scope,
		Results:    results,
	}

	if opts.JSONOutput {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		return enc.Encode(report)
	}

	renderSetupReport(stdout, report)
	return nil
}

func resolveSetupBinaryPath(explicit string, targetOS string, wslHost bool) string {
	if trimmed := strings.TrimSpace(explicit); trimmed != "" {
		return trimmed
	}
	if p, err := exec.LookPath("gdocumentor"); err == nil && strings.TrimSpace(p) != "" {
		if abs, absErr := filepath.Abs(p); absErr == nil {
			return abs
		}
		return p
	}
	if exe, err := os.Executable(); err == nil && strings.TrimSpace(exe) != "" {
		base := strings.ToLower(filepath.Base(exe))
		if strings.HasPrefix(base, "gdocumentor") && !strings.Contains(base, ".test") {
			if abs, absErr := filepath.Abs(exe); absErr == nil {
				return abs
			}
			return exe
		}
	}
	home, _ := os.UserHomeDir()
	if home != "" {
		candidates := []string{
			filepath.Join(home, "go", "bin", "gdocumentor"),
			filepath.Join(home, ".local", "bin", "gdocumentor"),
		}
		if targetOS == "windows" && !wslHost {
			candidates = []string{
				filepath.Join(home, "go", "bin", "gdocumentor.exe"),
				filepath.Join(home, ".local", "bin", "gdocumentor.exe"),
			}
		}
		for _, c := range candidates {
			if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
				return c
			}
		}
		if targetOS == "windows" && !wslHost {
			return filepath.Join(home, ".local", "bin", "gdocumentor.exe")
		}
		return filepath.Join(home, ".local", "bin", "gdocumentor")
	}
	return "gdocumentor"
}

func resolveWindowsHostHome(explicitWinUser string) (string, string, error) {
	mntUsers := os.Getenv("GDOCUMENTOR_WSL_USERS_ROOT")
	if strings.TrimSpace(mntUsers) == "" {
		mntUsers = "/mnt/c/Users"
	}

	user := strings.TrimSpace(explicitWinUser)
	if user == "" {
		user = strings.TrimSpace(os.Getenv("GDOCUMENTOR_WIN_USER"))
	}

	if user != "" {
		candidate := filepath.Join(mntUsers, user)
		return user, candidate, nil
	}

	// Auto-detect from /mnt/c/Users entries
	entries, err := os.ReadDir(mntUsers)
	if err != nil {
		return "", "", fmt.Errorf("failed to inspect Windows users directory %q for --wsl-host (pass --win-user <WindowsUsername> or ensure /mnt/c is mounted): %w", mntUsers, err)
	}

	skipNames := map[string]bool{
		"public":       true,
		"default":      true,
		"default user": true,
		"all users":    true,
		"desktop.ini":  true,
	}

	// Prefer matching WSL $USER if it exists in /mnt/c/Users
	wslUser := strings.TrimSpace(os.Getenv("USER"))
	if wslUser != "" {
		for _, e := range entries {
			if e.IsDir() && strings.EqualFold(e.Name(), wslUser) {
				return e.Name(), filepath.Join(mntUsers, e.Name()), nil
			}
		}
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if skipNames[strings.ToLower(name)] || strings.HasPrefix(name, ".") {
			continue
		}
		return name, filepath.Join(mntUsers, name), nil
	}

	return "", "", fmt.Errorf("could not automatically determine Windows username under %q; please pass --win-user <WindowsUsername>", mntUsers)
}

func resolveSetupUserHome(targetOS string, wslHost bool, winHomeDir string) string {
	if wslHost && strings.TrimSpace(winHomeDir) != "" {
		return winHomeDir
	}
	if envHome := strings.TrimSpace(os.Getenv("GDOCUMENTOR_SETUP_HOME")); envHome != "" {
		return envHome
	}
	if targetOS == "windows" {
		if up := strings.TrimSpace(os.Getenv("USERPROFILE")); up != "" {
			return up
		}
	}
	if h, err := os.UserHomeDir(); err == nil && strings.TrimSpace(h) != "" {
		return h
	}
	return os.Getenv("HOME")
}

func resolveAppDataDir(targetOS string, wslHost bool, homeDir string) string {
	if wslHost {
		return filepath.Join(homeDir, "AppData", "Roaming")
	}
	if envAppData := strings.TrimSpace(os.Getenv("GDOCUMENTOR_SETUP_APPDATA")); envAppData != "" {
		return envAppData
	}
	if targetOS == "windows" {
		if ad := strings.TrimSpace(os.Getenv("APPDATA")); ad != "" {
			return ad
		}
		return filepath.Join(homeDir, "AppData", "Roaming")
	}
	if targetOS == "darwin" {
		return filepath.Join(homeDir, "Library", "Application Support")
	}
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		return xdg
	}
	return filepath.Join(homeDir, ".config")
}

type pluginAssets struct {
	skillMD     []byte
	runSh       []byte
	runCmd      []byte
	ruleMD      []byte
	coachAgent  []byte
}

func loadPluginAssets(projectDir string) *pluginAssets {
	a := &pluginAssets{
		skillMD:    []byte(defaultEmbeddedSkillMD),
		runSh:      []byte(defaultEmbeddedRunSh),
		runCmd:     []byte(defaultEmbeddedRunCmd),
		ruleMD:     []byte(defaultEmbeddedRuleMD),
		coachAgent: []byte(defaultEmbeddedCoachAgentMD),
	}

	// Try to load live files from repository if available
	searchRoots := []string{projectDir}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		searchRoots = append(searchRoots, exeDir, filepath.Dir(exeDir))
	}

	for _, root := range searchRoots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(root, "skills", "documentor-ai", "SKILL.md")); err == nil && len(b) > 0 {
			a.skillMD = b
		}
		if b, err := os.ReadFile(filepath.Join(root, "skills", "documentor-ai", "scripts", "run_documentor.sh")); err == nil && len(b) > 0 {
			a.runSh = b
		}
		if b, err := os.ReadFile(filepath.Join(root, "skills", "documentor-ai", "scripts", "run_documentor.cmd")); err == nil && len(b) > 0 {
			a.runCmd = b
		}
		if b, err := os.ReadFile(filepath.Join(root, "rules", "gdocumentor-workflow.md")); err == nil && len(b) > 0 {
			a.ruleMD = b
		}
		if b, err := os.ReadFile(filepath.Join(root, "agents", "gdocumentor-coach.md")); err == nil && len(b) > 0 {
			a.coachAgent = b
		}
	}
	return a
}

type agentTargetPaths struct {
	mcpConfigPath string
	mcpTopKey     string // "mcpServers" or "servers" (for VS Code)
	isVSCode      bool
	skillDirs     []string
	ruleFiles     []string
	isCursorMDC   bool
	isCopilotInst bool
	agentFiles    []string
	notes         string
}

func resolveAgentTargetPaths(agent, scope, targetOS string, wslHost bool, homeDir, projectDir string) agentTargetPaths {
	effectiveOS := targetOS
	if wslHost {
		effectiveOS = "windows"
	}
	appData := resolveAppDataDir(effectiveOS, wslHost, homeDir)

	switch agent {
	case "antigravity":
		if scope == "project" {
			return agentTargetPaths{
				mcpConfigPath: filepath.Join(projectDir, "mcp_config.json"),
				mcpTopKey:     "mcpServers",
				skillDirs: []string{
					filepath.Join(projectDir, "skills", "documentor-ai"),
					filepath.Join(projectDir, ".agents", "skills", "documentor-ai"),
				},
				ruleFiles:  []string{filepath.Join(projectDir, "rules", "gdocumentor-workflow.md")},
				agentFiles: []string{filepath.Join(projectDir, "agents", "gdocumentor-coach.md")},
				notes:      "Antigravity workspace plugin & MCP configuration",
			}
		}
		return agentTargetPaths{
			mcpConfigPath: filepath.Join(homeDir, ".gemini", "config", "mcp_config.json"),
			mcpTopKey:     "mcpServers",
			skillDirs:     []string{filepath.Join(homeDir, ".gemini", "skills", "documentor-ai")},
			notes:         "Antigravity user-scope MCP & skill configuration (or run 'agy plugin install https://github.com/tanaikech/gdocumentor')",
		}

	case "claude-desktop":
		// Claude Desktop only has user-level claude_desktop_config.json
		return agentTargetPaths{
			mcpConfigPath: filepath.Join(appData, "Claude", "claude_desktop_config.json"),
			mcpTopKey:     "mcpServers",
			notes:         "Claude Desktop MCP server configuration (restart Claude Desktop to load tools)",
		}

	case "claude-code":
		if scope == "project" {
			return agentTargetPaths{
				mcpConfigPath: filepath.Join(projectDir, ".mcp.json"),
				mcpTopKey:     "mcpServers",
				skillDirs:     []string{filepath.Join(projectDir, ".claude", "skills", "documentor-ai")},
				agentFiles:    []string{filepath.Join(projectDir, ".claude", "agents", "gdocumentor-coach.md")},
				notes:         "Claude Code project MCP (.mcp.json), skill (.claude/skills), & subagent (.claude/agents)",
			}
		}
		return agentTargetPaths{
			mcpConfigPath: filepath.Join(homeDir, ".claude.json"),
			mcpTopKey:     "mcpServers",
			skillDirs:     []string{filepath.Join(homeDir, ".claude", "skills", "documentor-ai")},
			agentFiles:    []string{filepath.Join(homeDir, ".claude", "agents", "gdocumentor-coach.md")},
			notes:         "Claude Code user MCP (~/.claude.json), skill (~/.claude/skills), & subagent (~/.claude/agents)",
		}

	case "cursor":
		if scope == "project" {
			return agentTargetPaths{
				mcpConfigPath: filepath.Join(projectDir, ".cursor", "mcp.json"),
				mcpTopKey:     "mcpServers",
				skillDirs:     []string{filepath.Join(projectDir, ".cursor", "skills", "documentor-ai")},
				ruleFiles:     []string{filepath.Join(projectDir, ".cursor", "rules", "gdocumentor-workflow.mdc")},
				isCursorMDC:   true,
				notes:         "Cursor project MCP (.cursor/mcp.json), skill (.cursor/skills), & rule (.cursor/rules)",
			}
		}
		return agentTargetPaths{
			mcpConfigPath: filepath.Join(homeDir, ".cursor", "mcp.json"),
			mcpTopKey:     "mcpServers",
			skillDirs:     []string{filepath.Join(homeDir, ".cursor", "skills", "documentor-ai")},
			notes:         "Cursor global MCP (~/.cursor/mcp.json) & skill (~/.cursor/skills)",
		}

	case "vscode":
		if scope == "project" {
			return agentTargetPaths{
				mcpConfigPath: filepath.Join(projectDir, ".vscode", "mcp.json"),
				mcpTopKey:     "servers",
				isVSCode:      true,
				skillDirs:     []string{filepath.Join(projectDir, ".github", "skills", "documentor-ai")},
				ruleFiles:     []string{filepath.Join(projectDir, ".github", "instructions", "gdocumentor.instructions.md")},
				isCopilotInst: true,
				notes:         "VS Code / GitHub Copilot workspace MCP (.vscode/mcp.json), skill (.github/skills), & instructions",
			}
		}
		return agentTargetPaths{
			mcpConfigPath: filepath.Join(appData, "Code", "User", "mcp.json"),
			mcpTopKey:     "servers",
			isVSCode:      true,
			skillDirs:     []string{filepath.Join(homeDir, ".copilot", "skills", "documentor-ai")},
			notes:         "VS Code / GitHub Copilot global MCP (Code/User/mcp.json) & skill (~/.copilot/skills)",
		}

	case "windsurf":
		if scope == "project" {
			return agentTargetPaths{
				mcpConfigPath: filepath.Join(homeDir, ".codeium", "windsurf", "mcp_config.json"),
				mcpTopKey:     "mcpServers",
				skillDirs:     []string{filepath.Join(projectDir, ".windsurf", "skills", "documentor-ai")},
				ruleFiles:     []string{filepath.Join(projectDir, ".windsurf", "rules", "gdocumentor-workflow.md")},
				notes:         "Windsurf MCP (~/.codeium/windsurf/mcp_config.json), workspace skill & rule (.windsurf/rules)",
			}
		}
		return agentTargetPaths{
			mcpConfigPath: filepath.Join(homeDir, ".codeium", "windsurf", "mcp_config.json"),
			mcpTopKey:     "mcpServers",
			skillDirs:     []string{filepath.Join(homeDir, ".codeium", "windsurf", "skills", "documentor-ai")},
			notes:         "Windsurf global MCP (~/.codeium/windsurf/mcp_config.json) & skill",
		}

	case "gemini-cli":
		if scope == "project" {
			return agentTargetPaths{
				mcpConfigPath: filepath.Join(projectDir, ".gemini", "settings.json"),
				mcpTopKey:     "mcpServers",
				skillDirs:     []string{filepath.Join(projectDir, ".gemini", "skills", "documentor-ai")},
				notes:         "Gemini CLI project settings (.gemini/settings.json) & skill (.gemini/skills)",
			}
		}
		return agentTargetPaths{
			mcpConfigPath: filepath.Join(homeDir, ".gemini", "settings.json"),
			mcpTopKey:     "mcpServers",
			skillDirs:     []string{filepath.Join(homeDir, ".gemini", "skills", "documentor-ai")},
			notes:         "Gemini CLI global settings (~/.gemini/settings.json) & skill (~/.gemini/skills)",
		}
	}

	return agentTargetPaths{}
}

func processAgentSetup(agent, scope, action, targetOS string, wslHost bool, homeDir, projectDir, mcpCmd string, mcpArgs []string, assets *pluginAssets) (SetupAgentResult, error) {
	tp := resolveAgentTargetPaths(agent, scope, targetOS, wslHost, homeDir, projectDir)
	res := SetupAgentResult{
		Agent:         agent,
		Scope:         scope,
		Action:        action,
		MCPConfigPath: tp.mcpConfigPath,
		Command:       mcpCmd,
		Args:          mcpArgs,
		WSLHostBridge: wslHost,
		Notes:         tp.notes,
	}

	for _, sd := range tp.skillDirs {
		res.SkillPaths = append(res.SkillPaths, filepath.Join(sd, "SKILL.md"))
	}
	res.RulePaths = append(res.RulePaths, tp.ruleFiles...)
	res.AgentPaths = append(res.AgentPaths, tp.agentFiles...)

	switch action {
	case "status":
		res.MCPInstalled = checkMCPInstalled(tp.mcpConfigPath, tp.mcpTopKey)
		res.SkillInstalled = checkSkillsInstalled(tp.skillDirs, tp.mcpConfigPath)
		if agent == "antigravity" && scope == "user" && !res.MCPInstalled {
			// Also detect installation via `agy plugin install` (~/.gemini/config/plugins/gdocumentor/plugin.json)
			pluginManifest := filepath.Join(homeDir, ".gemini", "config", "plugins", "gdocumentor", "plugin.json")
			if fi, err := os.Stat(pluginManifest); err == nil && !fi.IsDir() {
				res.MCPInstalled = true
				res.SkillInstalled = true
				res.MCPConfigPath = filepath.Join(homeDir, ".gemini", "config", "plugins", "gdocumentor", "mcp_config.json")
			}
		}
		return res, nil

	case "uninstall":
		if tp.mcpConfigPath != "" {
			if err := removeMCPServerEntry(tp.mcpConfigPath, tp.mcpTopKey); err != nil {
				return res, err
			}
		}
		for _, sd := range tp.skillDirs {
			_ = os.RemoveAll(sd)
		}
		for _, rf := range tp.ruleFiles {
			_ = os.Remove(rf)
		}
		for _, af := range tp.agentFiles {
			_ = os.Remove(af)
		}
		res.MCPInstalled = false
		res.SkillInstalled = false
		return res, nil

	case "install":
		if tp.mcpConfigPath != "" {
			if err := upsertMCPServerEntry(tp.mcpConfigPath, tp.mcpTopKey, tp.isVSCode, mcpCmd, mcpArgs); err != nil {
				return res, err
			}
			res.MCPInstalled = true
		}
		for _, sd := range tp.skillDirs {
			if err := writeSkillBundle(sd, assets); err != nil {
				return res, err
			}
		}
		res.SkillInstalled = len(tp.skillDirs) > 0 || agent == "claude-desktop"
		for _, rf := range tp.ruleFiles {
			if err := writeRuleFile(rf, assets.ruleMD, tp.isCursorMDC, tp.isCopilotInst); err != nil {
				return res, err
			}
		}
		for _, af := range tp.agentFiles {
			if err := writeFileCreatingDirs(af, assets.coachAgent, 0o644); err != nil {
				return res, err
			}
		}
		return res, nil
	}

	return res, nil
}

func checkMCPInstalled(configPath, topKey string) bool {
	if strings.TrimSpace(configPath) == "" {
		return false
	}
	data, err := os.ReadFile(configPath)
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return false
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return false
	}
	serversRaw, ok := root[topKey]
	if !ok {
		return false
	}
	serversMap, ok := serversRaw.(map[string]any)
	if !ok {
		return false
	}
	if _, exists := serversMap["gdocumentor_server"]; exists {
		return true
	}
	if _, exists := serversMap["gdocumentor"]; exists {
		return true
	}
	return false
}

func checkSkillsInstalled(skillDirs []string, mcpConfigPath string) bool {
	if len(skillDirs) == 0 {
		return checkMCPInstalled(mcpConfigPath, "mcpServers")
	}
	for _, sd := range skillDirs {
		fi, err := os.Stat(filepath.Join(sd, "SKILL.md"))
		if err != nil || fi.IsDir() {
			return false
		}
	}
	return true
}

func upsertMCPServerEntry(configPath, topKey string, isVSCode bool, command string, args []string) error {
	root := make(map[string]any)
	if existing, err := os.ReadFile(configPath); err == nil && len(strings.TrimSpace(string(existing))) > 0 {
		if uErr := json.Unmarshal(existing, &root); uErr != nil {
			// Backup malformed JSON before overwriting
			_ = os.WriteFile(configPath+".bak", existing, 0o644)
			root = make(map[string]any)
		}
	}

	var serversMap map[string]any
	if raw, ok := root[topKey]; ok {
		if m, mOk := raw.(map[string]any); mOk {
			serversMap = m
		}
	}
	if serversMap == nil {
		serversMap = make(map[string]any)
	}

	entry := map[string]any{
		"command": command,
		"args":    args,
	}
	if isVSCode {
		entry["type"] = "stdio"
	}

	serversMap["gdocumentor_server"] = entry
	root[topKey] = serversMap

	encoded, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize MCP config %q: %w", configPath, err)
	}
	encoded = append(encoded, '\n')
	return writeFileCreatingDirs(configPath, encoded, 0o644)
}

func removeMCPServerEntry(configPath, topKey string) error {
	existing, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(strings.TrimSpace(string(existing))) == 0 {
		return nil
	}
	var root map[string]any
	if err := json.Unmarshal(existing, &root); err != nil {
		return nil
	}
	if raw, ok := root[topKey]; ok {
		if serversMap, mOk := raw.(map[string]any); mOk {
			delete(serversMap, "gdocumentor_server")
			delete(serversMap, "gdocumentor")
			root[topKey] = serversMap
		}
	}
	encoded, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	return os.WriteFile(configPath, encoded, 0o644)
}

func writeSkillBundle(skillDir string, assets *pluginAssets) error {
	if err := writeFileCreatingDirs(filepath.Join(skillDir, "SKILL.md"), assets.skillMD, 0o644); err != nil {
		return err
	}
	if err := writeFileCreatingDirs(filepath.Join(skillDir, "scripts", "run_documentor.sh"), assets.runSh, 0o755); err != nil {
		return err
	}
	if err := writeFileCreatingDirs(filepath.Join(skillDir, "scripts", "run_documentor.cmd"), assets.runCmd, 0o755); err != nil {
		return err
	}
	return nil
}

func writeRuleFile(rulePath string, rawRule []byte, isCursorMDC bool, isCopilotInst bool) error {
	content := string(rawRule)
	if isCursorMDC && !strings.HasPrefix(strings.TrimSpace(content), "---") {
		header := "---\ndescription: Activate when proofreading, reviewing, summarizing, inspecting, drafting, managing, or editing Google Documents or local files/scripts using gdocumentor (DocuMentor AI).\nglobs:\nalwaysApply: false\n---\n\n"
		content = header + content
	} else if isCopilotInst && !strings.HasPrefix(strings.TrimSpace(content), "---") {
		header := "---\napplyTo: \"**\"\n---\n\n"
		content = header + content
	}
	return writeFileCreatingDirs(rulePath, []byte(content), 0o644)
}

func writeFileCreatingDirs(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory %q: %w", dir, err)
	}
	if err := os.WriteFile(path, data, perm); err != nil {
		return fmt.Errorf("failed to write file %q: %w", path, err)
	}
	return nil
}

func renderSetupReport(w io.Writer, report *SetupReport) {
	fmt.Fprintf(w, "================================================================================\n")
	fmt.Fprintf(w, " 🛠️  DocuMentor AI (gdocumentor) — Multi-Agent Plugin & MCP Setup (%s)\n", strings.ToUpper(report.Action))
	fmt.Fprintf(w, "================================================================================\n")
	fmt.Fprintf(w, "• Target OS:    %s\n", report.TargetOS)
	fmt.Fprintf(w, "• Scope:        %s\n", report.Scope)
	fmt.Fprintf(w, "• Binary Path:  %s\n", report.BinaryPath)
	if report.WSLHost {
		fmt.Fprintf(w, "• WSL2 Bridge:  ENABLED (Windows Host User: %s -> wsl.exe --exec %s mcp)\n", report.WinUser, report.BinaryPath)
	}
	fmt.Fprintf(w, "--------------------------------------------------------------------------------\n")
	for _, r := range report.Results {
		statusBadge := "✅ Configured"
		switch report.Action {
		case "status":
			if r.MCPInstalled && r.SkillInstalled {
				statusBadge = "✅ Installed (MCP + Skill)"
			} else if r.MCPInstalled {
				statusBadge = "🟡 Partial (MCP Installed)"
			} else {
				statusBadge = "⚪ Not Installed"
			}
		case "uninstall":
			statusBadge = "🗑️  Uninstalled"
		}
		fmt.Fprintf(w, "[%s] (%s) — %s\n", r.Agent, r.Scope, statusBadge)
		if r.MCPConfigPath != "" {
			fmt.Fprintf(w, "  • MCP Config: %s\n", r.MCPConfigPath)
			fmt.Fprintf(w, "  • Command:    %s %s\n", r.Command, strings.Join(r.Args, " "))
		}
		for _, sp := range r.SkillPaths {
			fmt.Fprintf(w, "  • Skill:      %s\n", sp)
		}
		for _, rp := range r.RulePaths {
			fmt.Fprintf(w, "  • Rule:       %s\n", rp)
		}
		for _, ap := range r.AgentPaths {
			fmt.Fprintf(w, "  • Subagent:   %s\n", ap)
		}
		if r.Notes != "" {
			fmt.Fprintf(w, "  • Note:       %s\n", r.Notes)
		}
	}
	fmt.Fprintf(w, "================================================================================\n")
}

const defaultEmbeddedRunSh = `#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." 2>/dev/null && pwd || pwd)"
if command -v gdocumentor >/dev/null 2>&1; then
  exec gdocumentor "$@"
fi
if [[ -x "${HOME}/.local/bin/gdocumentor" ]]; then
  exec "${HOME}/.local/bin/gdocumentor" "$@"
fi
if [[ -x "${HOME}/go/bin/gdocumentor" ]]; then
  exec "${HOME}/go/bin/gdocumentor" "$@"
fi
if [[ -x "${REPO_ROOT}/gdocumentor" ]]; then
  exec "${REPO_ROOT}/gdocumentor" "$@"
fi
echo '{"error":"gdocumentor binary not found. Run: gdocumentor setup or install gdocumentor in PATH."}' >&2
exit 1
`

const defaultEmbeddedRunCmd = `@echo off
setlocal
where gdocumentor >nul 2>nul
if %ERRORLEVEL% EQU 0 (
    gdocumentor %*
    exit /b %ERRORLEVEL%
)
if exist "%USERPROFILE%\.local\bin\gdocumentor.exe" (
    "%USERPROFILE%\.local\bin\gdocumentor.exe" %*
    exit /b %ERRORLEVEL%
)
if exist "%USERPROFILE%\go\bin\gdocumentor.exe" (
    "%USERPROFILE%\go\bin\gdocumentor.exe" %*
    exit /b %ERRORLEVEL%
)
echo {"error":"gdocumentor.exe not found in PATH."} 1>&2
exit /b 1
`

const defaultEmbeddedSkillMD = `---
name: documentor-ai
description: Proofread and review local manuscripts, multi-file source code, specific line/character ranges, or existing Google Documents, or directly create, edit, and inspect Google Documents (including rich Markdown, tables, and image insertion/replacement/resizing) using gdocumentor (DocuMentor AI Go CLI & MCP Server).
---

# DocuMentor AI (` + "`gdocumentor`" + `) — Multi-Agent Skill

## Execution Policy (MCP Server vs. CLI Tool)
1. **Default Mode — MCP Server (` + "`gdocumentor_server`" + `, ` + "`GEMINI_API_KEY`" + ` NOT required)**:
   - Always use the MCP tools (` + "`gdocumentor_proofread`" + `, ` + "`gdocumentor_summary`" + `, ` + "`gdocumentor_draft`" + `, ` + "`gdocumentor_edit`" + `, ` + "`gdocumentor_inspect`" + `, ` + "`gdocumentor_manage`" + `) by default.
   - In **Claude Desktop, Claude Code, Cursor, VS Code (GitHub Copilot), Windsurf, and Gemini CLI**, call the MCP tool directly (e.g. ` + "`gdocumentor_proofread`" + `). In **Google Antigravity**, call via ` + "`call_mcp_tool`" + ` (` + "`ServerName: \"gdocumentor_server\"`" + `).
   - **Two-Phase Host-AI Flow**:
     - **Phase 1**: Call ` + "`gdocumentor_proofread`" + ` (or ` + "`gdocumentor_summary`" + ` / ` + "`gdocumentor_draft`" + `) with target parameters (` + "`inputs`" + `, ` + "`targets`" + `, ` + "`docId`" + `, ` + "`mode`" + `, ` + "`lang`" + `, etc.). It returns ` + "`status: \"needs_agent_inference\"`" + ` and ` + "`preparedContext`" + ` (` + "`systemInstruction`" + `, ` + "`userPrompt`" + `, ` + "`responseSchema`" + `, ` + "`documentId`" + `).
     - **Phase 2**: Generate the JSON matching ` + "`responseSchema`" + ` using your own AI reasoning, then call the same MCP tool again passing ` + "`aiResult`" + ` (and ` + "`docId: preparedContext.documentId`" + `, ` + "`skipDocInsert: true`" + ` if a new Google Doc was created in Phase 1).
2. **CLI Tool Mode (ONLY when explicitly requested in the user's prompt — ` + "`GEMINI_API_KEY`" + ` required)**:
   - Run the ` + "`gdocumentor`" + ` CLI binary via shell ONLY when the user explicitly asks to run ` + "`gdocumentor`" + ` as a CLI command.
`

const defaultEmbeddedRuleMD = `# DocuMentor AI (` + "`gdocumentor`" + `) — Workflow Rule

1. **Default to MCP Server (` + "`gdocumentor_server`" + `, No ` + "`GEMINI_API_KEY`" + ` Required)**:
   - Use MCP tools (` + "`gdocumentor_proofread`" + `, ` + "`gdocumentor_summary`" + `, ` + "`gdocumentor_draft`" + `, ` + "`gdocumentor_edit`" + `, ` + "`gdocumentor_inspect`" + `, ` + "`gdocumentor_manage`" + `) by default.
   - Follow the two-phase Host-AI flow: Phase 1 returns ` + "`preparedContext`" + `, your own AI model generates the JSON matching ` + "`responseSchema`" + `, and Phase 2 applies ` + "`aiResult`" + ` / ` + "`aiSummaryResult`" + ` / ` + "`generatedDraft`" + ` to Google Docs.
2. **CLI Tool Only When Explicitly Requested (` + "`GEMINI_API_KEY`" + ` Required)**:
   - Only invoke the ` + "`gdocumentor`" + ` CLI command in the terminal when the user's prompt explicitly asks to use ` + "`gdocumentor`" + ` as a CLI tool.
`

const defaultEmbeddedCoachAgentMD = `---
name: gdocumentor-coach
description: Interactive Google Docs proofreading and code-review coach powered by gdocumentor (DocuMentor AI).
---

You are **gdocumentor-coach**, an interactive Writing & Code Review Coach powered by ` + "`gdocumentor`" + `.
Use the ` + "`gdocumentor_server`" + ` MCP tools (` + "`gdocumentor_proofread`" + `, ` + "`gdocumentor_summary`" + `, ` + "`gdocumentor_draft`" + `, ` + "`gdocumentor_edit`" + `, ` + "`gdocumentor_inspect`" + `, ` + "`gdocumentor_manage`" + `) by default via the two-phase Host-AI workflow (0 internal Gemini API tokens). Only run the ` + "`gdocumentor`" + ` CLI binary when the user explicitly requests CLI tool execution in their prompt.
`
