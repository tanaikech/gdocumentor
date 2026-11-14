package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tanaikech/gdocumentor/internal/auth"
	"github.com/tanaikech/gdocumentor/internal/gdocs"
	"github.com/tanaikech/gdocumentor/internal/gemini"
	"github.com/tanaikech/gdocumentor/internal/utf16idx"
	"github.com/tanaikech/gdocumentor/internal/workspace"
)

// Version is the semantic version of the DocuMentor AI Go CLI & Agent Skill.
var Version = "1.0.1"

// Options holds all parsed CLI flags, subcommand settings, and Agent Skill JSON input parameters.
type Options struct {
	Command         string   `json:"command"`         // "proofread" (default), "summary", "draft", "manage", "inspect", "edit"
	RepoURL         string   `json:"repoUrl"`         // --repo GitHub repository URL to shallow-clone as workspace
	Inputs          []string `json:"inputs"`          // -i / --input or positional file/dir/URL paths
	Targets         []string `json:"targets"`         // -t / --target e.g. "main.go:15-45", "article.md@100:250", or GitHub URL#L15-L45
	RangeSpec       string   `json:"range"`           // --range e.g. "15-45" or "100:250"
	Contexts        []string `json:"contexts"`        // -c / --context reference files/dirs
	AutoDeps        bool     `json:"autoDeps"`        // --auto-deps (default true) / --no-auto-deps
	Rules           []string `json:"rules"`           // -r / --rules local RAG rule files
	RulesDir        string   `json:"rulesDir"`        // --rules-dir local RAG rule directory
	DriveRAG        string   `json:"driveRag"`        // --drive-rag Google Drive folder/file ID or URL
	Persona         string   `json:"persona"`         // --persona / --role custom AI persona & positioning
	DocID           string   `json:"docId"`           // -d / --doc-id existing Google Doc ID or URL
	Title           string   `json:"title"`           // --title title for newly created Google Doc
	ParentFolder    string   `json:"parentFolder"`    // --parent-folder Drive folder ID/URL for new Google Doc
	TabID           string   `json:"tabId"`           // --tab-id target Google Docs tab ID
	SummaryTab      bool     `json:"summaryTab"`      // --summary-tab / --summary-tag / --doc-summary: create/update Coaching Summary Tab in Google Docs
	SummaryTabName  string   `json:"summaryTabName"`  // --summary-tab-name / --summary-tag-name: custom title for the Google Docs Summary Tab
	Mode            string   `json:"mode"`            // -m / --mode: "suggest" (default) or "hint"
	IncludeResolved bool     `json:"includeResolved"` // --include-resolved (default true) / --no-include-resolved
	Model           string   `json:"model"`           // --model: "gemini-3.8-flash" (default), "gemini-4-hydrogen", "gemini-4-carbon", "gemini-4-argon", or custom
	Lang            string   `json:"lang"`            // -l / --lang: "en" (default) or "ja"
	Prompt          string   `json:"prompt"`          // -p / --prompt custom instructions or draft topic
	APIKey          string   `json:"apiKey"`          // --api-key Gemini API key override
	QuotaProject    string   `json:"quotaProject"`    // --quota-project X-Goog-User-Project override
	AccessToken     string   `json:"accessToken"`     // --access-token OAuth2 access token override
	NoAuth          bool     `json:"noAuth"`          // --no-auth / --no-access-token / --local: Gemini-only local file output mode without Google OAuth2
	SaveLocal       bool     `json:"saveLocal"`       // --save-local / --local-report: also save local Markdown report/draft when using an Access Token
	OutputPath      string   `json:"outputPath"`      // -o / --output: local file path to write proofread report, hints, summary, or draft
	InputJSON       string   `json:"-"`               // --input-json structured JSON payload for Agent Skill calls
	JSONOutput      bool     `json:"json"`            // --json emit ONLY pure JSON to stdout
	AcceptAll       bool     `json:"acceptAll"`       // --accept-all (manage subcommand)
	RejectAll       bool     `json:"rejectAll"`       // --rejectAll (manage subcommand)
	DeleteComments  bool     `json:"deleteComments"`  // --delete-comments (manage subcommand)
	ResetRecord     bool     `json:"resetRecord"`     // --reset-record (manage subcommand: reset Coach Decision Record)
	DeleteDoc       bool     `json:"deleteDoc"`       // --delete-doc (manage/edit subcommand: permanently delete Google Doc on Drive)

	// Direct Google Docs editing options (edit / manage subcommands)
	CreateDoc       bool    `json:"createDoc,omitempty"`       // --create-doc / --create: create a new Google Doc
	EditText        string  `json:"text,omitempty"`            // --text / --insert / --insert-text: text to insert or write
	HasEditText     bool    `json:"-"`                         // true if --text / --insert was explicitly specified
	Markdown        bool    `json:"markdown,omitempty"`        // --markdown / --md: convert Markdown syntax to native Google Docs elements
	ExplicitMD      *bool   `json:"-"`                         // non-nil if --markdown or --no-markdown / --raw-text was explicitly passed
	Position        string  `json:"position,omitempty"`        // --pos / --position / --prepend / --append: "start", "end", "@150", or paragraph number
	AtSpec          string  `json:"at,omitempty"`              // --at / --index: target UTF-16 index ("@150"), paragraph ("3"), "start", or "end"
	BeforeText      string  `json:"before,omitempty"`          // --before: insert immediately before matching text phrase
	AfterText       string  `json:"after,omitempty"`           // --after: insert immediately after matching text phrase
	BeforeParagraph int     `json:"beforeParagraph,omitempty"` // --before-paragraph / --before-para: insert before 1-based paragraph number
	AfterParagraph  int     `json:"afterParagraph,omitempty"`  // --after-paragraph / --after-para: insert after 1-based paragraph number
	ParagraphStyle  string  `json:"style,omitempty"`           // --style / --heading: optional paragraph style ("HEADING_1".."HEADING_6", "TITLE", "NORMAL_TEXT")
	FindText        string  `json:"find,omitempty"`            // --find / --search: text to search for replacement
	ReplaceText     string  `json:"replace,omitempty"`         // --replace / --replace-with: replacement text
	HasReplace      bool    `json:"-"`                         // true if --replace was explicitly specified
	ReplaceRange    string  `json:"replaceRange,omitempty"`    // --replace-range: replace specific range ("@100:250", "2-4", "all")
	ReplaceBody     bool    `json:"replaceBody,omitempty"`     // --replace-body / --replace-all: replace entire document body
	DeleteRange     string  `json:"deleteRange,omitempty"`     // --delete-range / --remove-range: delete specific range ("@100:250", "3", "2-5", "all")
	DeleteText      string  `json:"deleteText,omitempty"`      // --delete-text / --remove-text: delete specific text substring
	DeleteFlag      bool    `json:"-"`                         // --delete (pairs with --range as alias for --delete-range)
	ClearBody       bool    `json:"clearBody,omitempty"`       // --clear / --clear-body: clear entire document body
	InsertImage     string  `json:"insertImage,omitempty"`     // --insert-image / --image: local image file path or URL to insert
	ReplaceImage    string  `json:"replaceImage,omitempty"`    // --replace-image: target image ("VISUAL_1", "1", "kix.xxx", "@14", "all") to replace or resize
	ResizeImage     string  `json:"resizeImage,omitempty"`     // --resize-image: target image ("VISUAL_1", "1", "kix.xxx", "@14", "all") to resize in place
	WithImage       string  `json:"withImage,omitempty"`       // --with-image: replacement image path or URL for --replace-image (optional when resizing)
	DeleteImage     string  `json:"deleteImage,omitempty"`     // --delete-image / --remove-image: target image ("VISUAL_1", "1", "kix.xxx", "@14", "all") to delete
	ImageWidth      float64 `json:"imageWidth,omitempty"`      // --image-width: optional image width in PT
	ImageHeight     float64 `json:"imageHeight,omitempty"`     // --image-height: optional image height in PT
	MatchCase       *bool   `json:"matchCase,omitempty"`       // --match-case (default true) / --ignore-case (false)
	Occurrence      int     `json:"occurrence,omitempty"`      // --occurrence: 1-based occurrence number (0 = all)
	EditSuggest     bool    `json:"editSuggest,omitempty"`     // --suggest (in edit mode: apply edits as WriteMode.SUGGEST)

	// Host-AI / Agent-Side LLM execution options (MCP Server default or CLI --prepare-only / --ai-result)
	PrepareOnly       bool                    `json:"prepareOnly,omitempty"`       // --prepare-only: execute Step 1 and return PreparedContext for host AI agent (0 Gemini tokens)
	UseAgentAI        bool                    `json:"useAgentAi,omitempty"`        // --use-agent-ai: use host AI agent instead of internal Gemini API
	UseInternalGemini bool                    `json:"useInternalGemini,omitempty"` // --use-internal-gemini: force internal Gemini API even in MCP mode
	SkipDocInsert     bool                    `json:"skipDocInsert,omitempty"`     // --skip-doc-insert: skip re-inserting local file text when docId already has text from Phase 1
	AIResult          *gemini.ProofreadResult `json:"aiResult,omitempty"`          // --ai-result: pre-generated ProofreadResult JSON from host AI agent
	AISummaryResult   *gemini.SummaryResult   `json:"aiSummaryResult,omitempty"`   // --ai-summary-result: pre-generated SummaryResult JSON from host AI agent
	GeneratedDraft    string                  `json:"generatedDraft,omitempty"`    // --generated-draft: pre-generated initial draft text from host AI agent
	FromMCP           bool                    `json:"-"`                           // true when invoked via MCP server

	// Multi-Agent Setup / Installer options (setup subcommand)
	SetupAll    bool   `json:"all,omitempty"`        // -a / --all / --install-all: target all supported AI agents
	AgentTarget string `json:"agent,omitempty"`      // --agent / --agents / --client: target AI agent(s) ("all", "antigravity", "claude-desktop", "claude-code", "cursor", "vscode", "windsurf", "gemini-cli")
	SetupScope  string `json:"scope,omitempty"`      // --scope: "user" (default), "project", or "both"
	WSLHost     bool   `json:"wslHost,omitempty"`    // --wsl-host: configure Windows host GUI apps (Claude Desktop, Cursor, VS Code, Windsurf) from inside WSL2 via wsl.exe --exec
	WSLDistro   string `json:"wslDistro,omitempty"`  // --wsl-distro: optional WSL distribution name for wsl.exe -d <distro> --exec
	WinUser     string `json:"winUser,omitempty"`    // --win-user: optional Windows host username override for /mnt/c/Users/<WinUser>
	BinaryPath  string `json:"binaryPath,omitempty"` // --binary-path: explicit gdocumentor binary path to register in MCP configs
	SetupStatus bool   `json:"status,omitempty"`     // --status: show installation status across AI agents
	Uninstall   bool   `json:"uninstall,omitempty"`  // --uninstall: cleanly remove gdocumentor MCP server & skill entries

	ShowHelp    bool   `json:"-"`
	HelpTopic   string `json:"-"`
	ShowVersion bool   `json:"-"`

	// Test hooks, reverse-proxy override, and working directory configuration
	DocsBaseURL   string `json:"-"`
	DriveBaseURL  string `json:"-"`
	GeminiBaseURL string `json:"geminiBaseUrl,omitempty"` // --gemini-base-url: optional custom Gemini API endpoint / reverse proxy URL
	WorkDir       string `json:"workDir,omitempty"` // -w / --work-dir: working directory for resolving relative input, rule, and output paths
	StdinContent  string `json:"stdinContent,omitempty"`
}

// ParseArgs parses raw command-line arguments (allowing flags and positional arguments in any order)
// and merges any `--input-json` payload.
func ParseArgs(args []string) (*Options, error) {
	opts := &Options{
		Command:         "proofread",
		AutoDeps:        true,
		Mode:            "suggest",
		IncludeResolved: true,
		Lang:            "en",
	}

	if len(args) == 0 {
		opts.ShowHelp = true
		return opts, nil
	}

	explicitCommand := false
	if len(args) > 0 {
		first := strings.TrimSpace(args[0])
		switch first {
		case "proofread", "summary", "draft", "manage", "inspect", "edit", "mcp":
			opts.Command = first
			explicitCommand = true
			args = args[1:]
			if len(args) == 0 && opts.Command != "mcp" {
				opts.ShowHelp = true
				opts.HelpTopic = opts.Command
				return opts, nil
			}
		case "setup", "install-agent", "install-mcp", "install-skill":
			opts.Command = "setup"
			explicitCommand = true
			args = args[1:]
			if len(args) == 0 {
				opts.ShowHelp = true
				opts.HelpTopic = "setup"
				return opts, nil
			}
		case "--mcp":
			opts.Command = "mcp"
			explicitCommand = true
			args = args[1:]
		case "version", "--version", "-v":
			opts.ShowVersion = true
			return opts, nil
		case "help", "--help", "-h":
			opts.ShowHelp = true
			if len(args) > 1 {
				if topic := normalizeHelpTopic(args[1]); topic != "" {
					opts.HelpTopic = topic
				}
			}
			return opts, nil
		}
	}

	var explicitJSONFlag *bool

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			opts.Inputs = append(opts.Inputs, args[i+1:]...)
			break
		}

		if !strings.HasPrefix(arg, "-") || arg == "-" {
			opts.Inputs = append(opts.Inputs, arg)
			continue
		}

		// Split --flag=value if present
		flagName := arg
		flagVal := ""
		hasInlineVal := false
		if eqIdx := strings.IndexByte(arg, '='); eqIdx > 0 {
			flagName = arg[:eqIdx]
			flagVal = arg[eqIdx+1:]
			hasInlineVal = true
		}

		nextVal := func() (string, error) {
			if hasInlineVal {
				return flagVal, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("flag %s requires a value", flagName)
			}
			i++
			return args[i], nil
		}

		switch flagName {
		case "-h", "--help":
			opts.ShowHelp = true
			if explicitCommand && opts.HelpTopic == "" {
				opts.HelpTopic = opts.Command
			}
		case "-v", "--version":
			opts.ShowVersion = true
		case "-w", "--work-dir", "--workdir", "--cwd":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.WorkDir = strings.TrimSpace(v)
		case "--repo", "--github-repo":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.RepoURL = v
		case "-i", "--input", "-f", "--file":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.Inputs = append(opts.Inputs, splitCommaOrKeep(v)...)
		case "-t", "--target":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.Targets = append(opts.Targets, v)
		case "--range":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.RangeSpec = v
		case "-c", "--context":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.Contexts = append(opts.Contexts, splitCommaOrKeep(v)...)
		case "--auto-deps":
			if hasInlineVal {
				opts.AutoDeps = parseBoolStr(flagVal, true)
			} else {
				opts.AutoDeps = true
			}
		case "--no-auto-deps":
			opts.AutoDeps = false
		case "-r", "--rules":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.Rules = append(opts.Rules, splitCommaOrKeep(v)...)
		case "--rules-dir":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.RulesDir = v
		case "--drive-rag":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.DriveRAG = v
		case "--persona", "--role":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.Persona = v
		case "-d", "--doc-id", "--doc":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.DocID = v
		case "--title":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.Title = v
		case "--parent-folder", "--folder":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.ParentFolder = v
		case "--tab-id", "--tab":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.TabID = v
		case "--summary-tab", "--summary-tag", "--doc-summary":
			if hasInlineVal {
				switch strings.ToLower(strings.TrimSpace(flagVal)) {
				case "1", "true", "yes", "on":
					opts.SummaryTab = true
				case "0", "false", "no", "off":
					opts.SummaryTab = false
				default:
					opts.SummaryTab = true
					opts.SummaryTabName = strings.TrimSpace(flagVal)
				}
			} else {
				opts.SummaryTab = true
			}
		case "--no-summary-tab", "--no-summary-tag":
			opts.SummaryTab = false
		case "--summary-tab-name", "--summary-tag-name":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.SummaryTabName = strings.TrimSpace(v)
			opts.SummaryTab = true
		case "-m", "--mode":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.Mode = strings.ToLower(strings.TrimSpace(v))
		case "--hint", "--hint-only":
			opts.Mode = "hint"
		case "--include-resolved":
			if hasInlineVal {
				opts.IncludeResolved = parseBoolStr(flagVal, true)
			} else {
				opts.IncludeResolved = true
			}
		case "--no-include-resolved":
			opts.IncludeResolved = false
		case "--model":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.Model = v
		case "-l", "--lang", "--language":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.Lang = strings.ToLower(strings.TrimSpace(v))
		case "-p", "--prompt":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.Prompt = v
		case "--api-key":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.APIKey = v
		case "--gemini-base-url":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.GeminiBaseURL = strings.TrimSpace(v)
		case "--quota-project":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.QuotaProject = v
		case "--access-token":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.AccessToken = v
		case "--no-auth", "--no-access-token", "--local":
			if hasInlineVal {
				opts.NoAuth = parseBoolStr(flagVal, true)
			} else {
				opts.NoAuth = true
			}
		case "--save-local", "--local-report", "--local-output":
			if hasInlineVal {
				opts.SaveLocal = parseBoolStr(flagVal, true)
			} else {
				opts.SaveLocal = true
			}
		case "-o", "--output", "--out":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.OutputPath = v
		case "--input-json":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.InputJSON = v
		case "--json":
			b := true
			if hasInlineVal {
				b = parseBoolStr(flagVal, true)
			}
			opts.JSONOutput = b
			explicitJSONFlag = &b
		case "--accept-all":
			opts.AcceptAll = true
			if !explicitCommand {
				opts.Command = "manage"
			}
		case "--reject-all":
			opts.RejectAll = true
			if !explicitCommand {
				opts.Command = "manage"
			}
		case "--delete-comments":
			opts.DeleteComments = true
			if !explicitCommand {
				opts.Command = "manage"
			}
		case "--reset-record", "--reset-decision-record":
			opts.ResetRecord = true
			if !explicitCommand {
				opts.Command = "manage"
			}
		case "--delete-doc", "--delete-document":
			opts.DeleteDoc = true
			if !explicitCommand {
				opts.Command = "manage"
			}
		case "--create-doc", "--create-document", "--create", "--new-doc":
			if hasInlineVal {
				opts.CreateDoc = parseBoolStr(flagVal, true)
			} else {
				opts.CreateDoc = true
			}
			if !explicitCommand {
				opts.Command = "edit"
			}
		case "--text", "--insert", "--insert-text", "--write":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.EditText = v
			opts.HasEditText = true
			if !explicitCommand {
				opts.Command = "edit"
			}
		case "--prepend", "--start":
			opts.Position = "start"
			if !explicitCommand {
				opts.Command = "edit"
			}
		case "--append", "--end":
			opts.Position = "end"
			if !explicitCommand {
				opts.Command = "edit"
			}
		case "--pos", "--position":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.Position = strings.TrimSpace(v)
			if !explicitCommand {
				opts.Command = "edit"
			}
		case "--at":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.AtSpec = strings.TrimSpace(v)
			if !explicitCommand {
				opts.Command = "edit"
			}
		case "--index", "--offset":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			trimmedIdx := strings.TrimSpace(v)
			if trimmedIdx != "" && !strings.HasPrefix(trimmedIdx, "@") {
				trimmedIdx = "@" + trimmedIdx
			}
			opts.AtSpec = trimmedIdx
			if !explicitCommand {
				opts.Command = "edit"
			}
		case "--before", "--insert-before":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.BeforeText = v
			if !explicitCommand {
				opts.Command = "edit"
			}
		case "--after", "--insert-after":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.AfterText = v
			if !explicitCommand {
				opts.Command = "edit"
			}
		case "--before-paragraph", "--before-para", "--before-line":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			n, convErr := strconv.Atoi(strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(v)), "l"), "#"))
			if convErr != nil || n < 1 {
				return nil, fmt.Errorf("invalid %s value %q: must be a positive 1-based paragraph number", flagName, v)
			}
			opts.BeforeParagraph = n
			if !explicitCommand {
				opts.Command = "edit"
			}
		case "--after-paragraph", "--after-para", "--after-line":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			n, convErr := strconv.Atoi(strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(v)), "l"), "#"))
			if convErr != nil || n < 1 {
				return nil, fmt.Errorf("invalid %s value %q: must be a positive 1-based paragraph number", flagName, v)
			}
			opts.AfterParagraph = n
			if !explicitCommand {
				opts.Command = "edit"
			}
		case "--style", "--heading":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.ParagraphStyle = strings.TrimSpace(v)
			if !explicitCommand {
				opts.Command = "edit"
			}
		case "--find", "--search":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.FindText = v
			if !explicitCommand {
				opts.Command = "edit"
			}
		case "--replace", "--replace-with":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.ReplaceText = v
			opts.HasReplace = true
			if !explicitCommand {
				opts.Command = "edit"
			}
		case "--replace-range":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.ReplaceRange = strings.TrimSpace(v)
			if !explicitCommand {
				opts.Command = "edit"
			}
		case "--replace-body", "--replace-all", "--overwrite-body":
			if hasInlineVal {
				opts.ReplaceBody = parseBoolStr(flagVal, true)
			} else {
				opts.ReplaceBody = true
			}
			if !explicitCommand {
				opts.Command = "edit"
			}
		case "--delete-range", "--remove-range":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.DeleteRange = strings.TrimSpace(v)
			if !explicitCommand {
				opts.Command = "edit"
			}
		case "--delete-text", "--remove-text":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.DeleteText = v
			if !explicitCommand {
				opts.Command = "edit"
			}
		case "--delete":
			if hasInlineVal {
				opts.DeleteRange = strings.TrimSpace(flagVal)
			} else {
				opts.DeleteFlag = true
			}
			if !explicitCommand {
				opts.Command = "edit"
			}
		case "--clear", "--clear-body":
			if hasInlineVal {
				opts.ClearBody = parseBoolStr(flagVal, true)
			} else {
				opts.ClearBody = true
			}
			if !explicitCommand {
				opts.Command = "edit"
			}
		case "--markdown", "--md":
			b := true
			if hasInlineVal {
				b = parseBoolStr(flagVal, true)
			}
			opts.Markdown = b
			opts.ExplicitMD = &b
			if !explicitCommand {
				opts.Command = "edit"
			}
		case "--no-markdown", "--raw-text":
			b := false
			opts.Markdown = false
			opts.ExplicitMD = &b
		case "--insert-image", "--image":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.InsertImage = strings.TrimSpace(v)
			if !explicitCommand {
				opts.Command = "edit"
			}
		case "--replace-image":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.ReplaceImage = strings.TrimSpace(v)
			if !explicitCommand {
				opts.Command = "edit"
			}
		case "--resize-image":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.ResizeImage = strings.TrimSpace(v)
			if !explicitCommand {
				opts.Command = "edit"
			}
		case "--with-image":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.WithImage = strings.TrimSpace(v)
			if !explicitCommand {
				opts.Command = "edit"
			}
		case "--delete-image", "--remove-image":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.DeleteImage = strings.TrimSpace(v)
			if !explicitCommand {
				opts.Command = "edit"
			}
		case "--image-width":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			w, convErr := strconv.ParseFloat(strings.TrimSuffix(strings.ToLower(strings.TrimSpace(v)), "pt"), 64)
			if convErr != nil || w <= 0 {
				return nil, fmt.Errorf("invalid --image-width %q: must be a positive number (points)", v)
			}
			opts.ImageWidth = w
		case "--image-height":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			h, convErr := strconv.ParseFloat(strings.TrimSuffix(strings.ToLower(strings.TrimSpace(v)), "pt"), 64)
			if convErr != nil || h <= 0 {
				return nil, fmt.Errorf("invalid --image-height %q: must be a positive number (points)", v)
			}
			opts.ImageHeight = h
		case "--match-case":
			b := true
			if hasInlineVal {
				b = parseBoolStr(flagVal, true)
			}
			opts.MatchCase = &b
		case "--ignore-case", "--no-match-case":
			b := false
			opts.MatchCase = &b
		case "--occurrence":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			if strings.EqualFold(strings.TrimSpace(v), "all") {
				opts.Occurrence = 0
			} else {
				n, convErr := strconv.Atoi(strings.TrimSpace(v))
				if convErr != nil || n < 0 {
					return nil, fmt.Errorf("invalid --occurrence %q: must be a non-negative integer or 'all'", v)
				}
				opts.Occurrence = n
			}
		case "--suggest":
			opts.EditSuggest = true
		case "--prepare-only", "--prepare":
			if hasInlineVal {
				opts.PrepareOnly = parseBoolStr(flagVal, true)
			} else {
				opts.PrepareOnly = true
			}
		case "--use-agent-ai", "--agent-ai", "--host-ai":
			if hasInlineVal {
				opts.UseAgentAI = parseBoolStr(flagVal, true)
			} else {
				opts.UseAgentAI = true
			}
		case "--use-internal-gemini", "--internal-gemini":
			if hasInlineVal {
				opts.UseInternalGemini = parseBoolStr(flagVal, true)
			} else {
				opts.UseInternalGemini = true
			}
		case "--skip-doc-insert":
			if hasInlineVal {
				opts.SkipDocInsert = parseBoolStr(flagVal, true)
			} else {
				opts.SkipDocInsert = true
			}
		case "--ai-result":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			raw := strings.TrimSpace(v)
			if after, ok := strings.CutPrefix(raw, "@"); ok {
				fp := after
				if !filepath.IsAbs(fp) && strings.TrimSpace(opts.WorkDir) != "" {
					fp = filepath.Join(opts.WorkDir, fp)
				}
				b, rErr := os.ReadFile(fp)
				if rErr != nil {
					return nil, fmt.Errorf("failed to read --ai-result file %q: %w", fp, rErr)
				}
				raw = string(b)
			}
			var res gemini.ProofreadResult
			if uErr := json.Unmarshal([]byte(raw), &res); uErr != nil {
				return nil, fmt.Errorf("failed to parse --ai-result JSON: %w", uErr)
			}
			opts.AIResult = &res
		case "--ai-summary-result":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			raw := strings.TrimSpace(v)
			if after, ok := strings.CutPrefix(raw, "@"); ok {
				fp := after
				if !filepath.IsAbs(fp) && strings.TrimSpace(opts.WorkDir) != "" {
					fp = filepath.Join(opts.WorkDir, fp)
				}
				b, rErr := os.ReadFile(fp)
				if rErr != nil {
					return nil, fmt.Errorf("failed to read --ai-summary-result file %q: %w", fp, rErr)
				}
				raw = string(b)
			}
			var res gemini.SummaryResult
			if uErr := json.Unmarshal([]byte(raw), &res); uErr != nil {
				return nil, fmt.Errorf("failed to parse --ai-summary-result JSON: %w", uErr)
			}
			opts.AISummaryResult = &res
		case "--generated-draft":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			raw := v
			if after, ok := strings.CutPrefix(strings.TrimSpace(v), "@"); ok {
				fp := after
				if !filepath.IsAbs(fp) && strings.TrimSpace(opts.WorkDir) != "" {
					fp = filepath.Join(opts.WorkDir, fp)
				}
				b, rErr := os.ReadFile(fp)
				if rErr != nil {
					return nil, fmt.Errorf("failed to read --generated-draft file %q: %w", fp, rErr)
				}
				raw = string(b)
			}
			opts.GeneratedDraft = raw
		case "--mcp":
			opts.Command = "mcp"
		case "-a", "--all", "--install-all":
			if hasInlineVal {
				opts.SetupAll = parseBoolStr(flagVal, true)
			} else {
				opts.SetupAll = true
			}
			if opts.SetupAll && opts.AgentTarget == "" {
				opts.AgentTarget = "all"
			}
			if !explicitCommand {
				opts.Command = "setup"
			}
		case "--agent", "--agents", "--client", "--clients":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			if opts.AgentTarget == "" {
				opts.AgentTarget = strings.TrimSpace(v)
			} else {
				opts.AgentTarget = opts.AgentTarget + "," + strings.TrimSpace(v)
			}
			if !explicitCommand {
				opts.Command = "setup"
			}
		case "--scope":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.SetupScope = strings.ToLower(strings.TrimSpace(v))
			if !explicitCommand {
				opts.Command = "setup"
			}
		case "--wsl-host", "--wsl", "--wsl-bridge":
			if hasInlineVal {
				opts.WSLHost = parseBoolStr(flagVal, true)
			} else {
				opts.WSLHost = true
			}
			if !explicitCommand {
				opts.Command = "setup"
			}
		case "--wsl-distro", "--distro":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.WSLDistro = strings.TrimSpace(v)
			if !explicitCommand {
				opts.Command = "setup"
			}
		case "--win-user", "--windows-user":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.WinUser = strings.TrimSpace(v)
			if !explicitCommand {
				opts.Command = "setup"
			}
		case "--binary-path", "--bin-path":
			v, err := nextVal()
			if err != nil {
				return nil, err
			}
			opts.BinaryPath = strings.TrimSpace(v)
			if !explicitCommand {
				opts.Command = "setup"
			}
		case "--status":
			if hasInlineVal {
				opts.SetupStatus = parseBoolStr(flagVal, true)
			} else {
				opts.SetupStatus = true
			}
			if !explicitCommand {
				opts.Command = "setup"
			}
		case "--uninstall", "--remove-agent":
			if hasInlineVal {
				opts.Uninstall = parseBoolStr(flagVal, true)
			} else {
				opts.Uninstall = true
			}
			if !explicitCommand {
				opts.Command = "setup"
			}
		default:
			return nil, fmt.Errorf("unknown flag: %s (run 'gdocumentor --help' for usage)", flagName)
		}
	}

	if explicitCommand && len(opts.Inputs) == 1 && strings.EqualFold(strings.TrimSpace(opts.Inputs[0]), "help") {
		opts.ShowHelp = true
		opts.HelpTopic = opts.Command
		opts.Inputs = nil
	}
	if opts.ShowHelp && opts.HelpTopic == "" && explicitCommand {
		opts.HelpTopic = opts.Command
	}

	// Merge --input-json payload if provided
	if strings.TrimSpace(opts.InputJSON) != "" {
		rawJSON := strings.TrimSpace(opts.InputJSON)
		if after, ok := strings.CutPrefix(rawJSON, "@"); ok {
			filePath := workspace.NormalizeCrossPlatformPath(after)
			if !filepath.IsAbs(filePath) && strings.TrimSpace(opts.WorkDir) != "" {
				filePath = filepath.Join(workspace.NormalizeCrossPlatformPath(opts.WorkDir), filePath)
			}
			data, err := os.ReadFile(filePath)
			if err != nil {
				return nil, fmt.Errorf("failed to read --input-json file %q: %w", filePath, err)
			}
			rawJSON = string(data)
		}
		if err := json.Unmarshal([]byte(rawJSON), opts); err != nil {
			return nil, fmt.Errorf("failed to parse --input-json payload: %w", err)
		}
		if explicitJSONFlag != nil {
			opts.JSONOutput = *explicitJSONFlag
		} else {
			opts.JSONOutput = true
		}
		applyJSONPresenceFlags([]byte(rawJSON), opts)
	}

	// Normalize cross-platform (Windows <-> WSL2 / Linux) paths across options
	opts.WorkDir = workspace.NormalizeCrossPlatformPath(opts.WorkDir)
	opts.OutputPath = workspace.NormalizeCrossPlatformPath(opts.OutputPath)
	opts.RulesDir = workspace.NormalizeCrossPlatformPath(opts.RulesDir)
	opts.InsertImage = workspace.NormalizeCrossPlatformPath(opts.InsertImage)
	opts.WithImage = workspace.NormalizeCrossPlatformPath(opts.WithImage)
	for idx, inp := range opts.Inputs {
		if inp != "-" {
			opts.Inputs[idx] = workspace.NormalizeCrossPlatformPath(inp)
		}
	}
	for idx, ctxPath := range opts.Contexts {
		opts.Contexts[idx] = workspace.NormalizeCrossPlatformPath(ctxPath)
	}
	for idx, rulePath := range opts.Rules {
		opts.Rules[idx] = workspace.NormalizeCrossPlatformPath(rulePath)
	}
	if strings.TrimSpace(opts.SummaryTabName) != "" {
		opts.SummaryTab = true
	}
	if opts.DeleteFlag && opts.DeleteRange == "" && strings.TrimSpace(opts.RangeSpec) != "" {
		opts.DeleteRange = strings.TrimSpace(opts.RangeSpec)
		opts.RangeSpec = ""
	}
	if opts.HasReplace && opts.FindText == "" && opts.ReplaceRange == "" && !opts.ReplaceBody && strings.TrimSpace(opts.RangeSpec) != "" {
		opts.ReplaceRange = strings.TrimSpace(opts.RangeSpec)
		opts.RangeSpec = ""
	}

	// Normalize Mode & Lang
	switch strings.ToLower(strings.TrimSpace(opts.Mode)) {
	case "", "suggest", "suggestion", "suggestions":
		opts.Mode = "suggest"
	case "hint", "hints", "hint-only", "coaching":
		opts.Mode = "hint"
	default:
		return nil, fmt.Errorf("invalid --mode %q: must be 'suggest' or 'hint'", opts.Mode)
	}

	opts.Lang = gemini.NormalizeLanguageCode(opts.Lang)

	return opts, nil
}

// Run executes the CLI application with strict stdout/stderr separation.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	opts, err := ParseArgs(args)
	if err != nil {
		return emitError(stdout, stderr, false, err)
	}
	if opts.ShowHelp {
		if opts.HelpTopic != "" {
			PrintSubcommandHelp(stdout, opts.HelpTopic)
		} else {
			PrintHelp(stdout)
		}
		return nil
	}
	if opts.ShowVersion {
		fmt.Fprintf(stdout, "gdocumentor version %s\n", Version)
		return nil
	}
	if opts.Command == "mcp" {
		return RunMCPServer(ctx, stdin, stdout, &mcpServerOverrides{
			DocsBaseURL:   opts.DocsBaseURL,
			DriveBaseURL:  opts.DriveBaseURL,
			GeminiBaseURL: opts.GeminiBaseURL,
			WorkDir:       opts.WorkDir,
			AccessToken:   opts.AccessToken,
			APIKey:        opts.APIKey,
			NoAuth:        opts.NoAuth,
		})
	}
	if opts.Command == "setup" {
		return ExecuteSetup(ctx, opts, stdout, stderr)
	}

	// Read from stdin when '-' is passed, or when stdin is piped/redirected (pipe '|', here-string '<<<',
	// here-document '<<EOF', or file redirect '< file').
	if opts.StdinContent == "" && stdin != nil {
		hasDash := false
		hasFileInputs := false
		for _, inp := range opts.Inputs {
			if inp == "-" {
				hasDash = true
			} else if strings.TrimSpace(inp) != "" {
				hasFileInputs = true
			}
		}
		isPureDeleteOrImageEdit := opts.Command == "edit" &&
			(opts.DeleteDoc || opts.ClearBody || opts.DeleteRange != "" || opts.DeleteText != "" ||
				opts.InsertImage != "" || opts.ReplaceImage != "" || opts.ResizeImage != "" || opts.DeleteImage != "") &&
			!opts.HasEditText && !opts.HasReplace && opts.ReplaceRange == "" && !opts.ReplaceBody && opts.FindText == ""

		shouldAutoReadStdin := false
		if isPipedStdin(stdin) {
			switch opts.Command {
			case "edit":
				if !isPureDeleteOrImageEdit && !opts.HasEditText && !hasFileInputs {
					shouldAutoReadStdin = true
				}
			case "proofread":
				if !hasFileInputs && len(opts.Targets) == 0 && opts.DocID == "" {
					shouldAutoReadStdin = true
				}
			}
		}

		if hasDash || shouldAutoReadStdin {
			data, readErr := io.ReadAll(stdin)
			if readErr == nil && len(data) > 0 {
				opts.StdinContent = string(data)
			}
		}
	}

	out, execErr := ExecuteOptions(ctx, opts, stderr)
	if execErr != nil {
		return emitError(stdout, stderr, opts.JSONOutput, execErr)
	}

	if opts.JSONOutput {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		return enc.Encode(out)
	}

	RenderCoachingReport(stdout, out)
	return nil
}

// ExecuteOptions runs the core DocuMentor AI pipeline according to opts and returns the structured CLIOutput.
func ExecuteOptions(ctx context.Context, opts *Options, stderr io.Writer) (*CLIOutput, error) {
	isAgentMode := opts.PrepareOnly || opts.UseAgentAI ||
		(opts.FromMCP && !opts.UseInternalGemini) ||
		opts.AIResult != nil || opts.AISummaryResult != nil || strings.TrimSpace(opts.GeneratedDraft) != ""

	resolvedModel := gemini.ResolveModelName(opts.Model)
	if isAgentMode && strings.TrimSpace(opts.Model) == "" {
		resolvedModel = "agent-ai"
	}
	hintOnly := opts.Mode == "hint"
	isJa := opts.Lang == "ja"

	authMgr := auth.NewManager(auth.Options{
		AccessToken:  opts.AccessToken,
		QuotaProject: opts.QuotaProject,
		GeminiAPIKey: opts.APIKey,
		Model:        resolvedModel,
		Lang:         opts.Lang,
	})

	// Check authentication readiness for banner display
	var bannerStatus BannerAuthStatus
	if !opts.JSONOutput && stderr != nil {
		requireGemini := !isAgentMode && opts.Command != "edit" && opts.Command != "manage" && opts.Command != "inspect"
		requireDrive := !opts.NoAuth && (strings.TrimSpace(opts.DriveRAG) != "" ||
			strings.TrimSpace(opts.ParentFolder) != "" ||
			opts.DeleteDoc ||
			strings.TrimSpace(opts.InsertImage) != "" ||
			strings.TrimSpace(opts.WithImage) != "" ||
			(opts.Command == "proofread" && strings.TrimSpace(opts.DocID) == "" && (len(opts.Inputs) > 0 || len(opts.Targets) > 0)))
		skipNet := opts.DocsBaseURL != "" || opts.DriveBaseURL != "" || opts.GeminiBaseURL != ""

		checkRes := authMgr.CheckAuthStatus(ctx, auth.AuthCheckOptions{
			NoAuth:         opts.NoAuth,
			RequireGemini:  requireGemini,
			RequireDrive:   requireDrive,
			SkipNetworkVal: skipNet,
		})
		if isAgentMode && checkRes.GeminiStatus == "BYPASSED" {
			checkRes.GeminiKeySource = "Agent AI"
		}
		bannerStatus = BannerAuthStatus{
			KeyState:   checkRes.GeminiStatus,
			HasKey:     checkRes.HasGeminiKey,
			KeySource:  checkRes.GeminiKeySource,
			OAuthState: checkRes.OAuthStatus,
			OAuthDesc:  checkRes.OAuthDetails,
		}
		RenderHeaderBanner(stderr, opts.Command, resolvedModel, opts.Mode, opts.Lang, bannerStatus)
	}

	// Spinner is strictly disabled when JSONOutput is true
	spinnerEnabled := !opts.JSONOutput && stderr != nil
	spin := NewSpinner(stderr, spinnerEnabled)
	defer spin.Stop("")

	// Validate --no-auth / --local constraints honestly upfront
	if opts.NoAuth {
		switch opts.Command {
		case "manage", "inspect", "summary", "edit":
			return nil, fmt.Errorf("subcommand %q operates on Google Docs and requires a Google OAuth2 Access Token (cannot be combined with --no-auth)", opts.Command)
		}
		if strings.TrimSpace(opts.DocID) != "" {
			return nil, errors.New("--doc-id (-d) requires a Google OAuth2 Access Token to access Google Docs API (cannot be combined with --no-auth)")
		}
		if strings.TrimSpace(opts.DriveRAG) != "" {
			return nil, errors.New("--drive-rag requires a Google OAuth2 Access Token to access Google Drive API (use local/URL -r / --rules-dir with --no-auth)")
		}
	}
	docsClient := gdocs.NewClient(authMgr)
	if opts.DocsBaseURL != "" || opts.DriveBaseURL != "" {
		docsClient.SetBaseURLs(opts.DocsBaseURL, opts.DriveBaseURL)
	}

	// 1. Handle 'edit', 'manage', or 'inspect' subcommand without requiring Gemini API
	if opts.Command == "edit" {
		return executeEditCommand(ctx, opts, docsClient, spin, resolvedModel, hintOnly, isJa)
	}
	if opts.Command == "manage" {
		return executeManageCommand(ctx, opts, docsClient, spin, resolvedModel, hintOnly, isJa)
	}
	if opts.Command == "inspect" {
		return executeInspectCommand(ctx, opts, docsClient, spin, resolvedModel, hintOnly, isJa)
	}

	// Initialize Gemini API client only when internal Gemini API execution is required
	var geminiClient *gemini.Client
	if !isAgentMode {
		var err error
		geminiClient, err = gemini.NewClient(opts.APIKey, resolvedModel)
		if err != nil {
			return nil, err
		}
		if opts.GeminiBaseURL != "" {
			geminiClient.SetBaseURL(opts.GeminiBaseURL)
		}
	}

	// 2. Handle 'draft' subcommand
	if opts.Command == "draft" {
		return executeDraftCommand(ctx, opts, docsClient, geminiClient, spin, resolvedModel, hintOnly, isJa)
	}

	// 3. Handle 'summary' subcommand
	if opts.Command == "summary" {
		return executeSummaryCommand(ctx, opts, docsClient, geminiClient, spin, resolvedModel, hintOnly, isJa)
	}

	// 4. Default: 'proofread' subcommand (New Document conversion/creation from local files OR Existing Google Doc review)
	return executeProofreadCommand(ctx, opts, docsClient, geminiClient, spin, resolvedModel, hintOnly, isJa)
}

func executeEditCommand(
	ctx context.Context,
	opts *Options,
	docsClient *gdocs.Client,
	spin *Spinner,
	resolvedModel string,
	hintOnly bool,
	isJa bool,
) (*CLIOutput, error) {
	docID := gdocs.ExtractDocumentID(opts.DocID)

	// Separate file inputs from '-' (stdin marker)
	var fileInputs []string
	for _, inp := range opts.Inputs {
		trimmed := strings.TrimSpace(inp)
		if trimmed == "" || trimmed == "-" {
			continue
		}
		fileInputs = append(fileInputs, trimmed)
	}

	// Resolve input text from: (1) --text, (2) -i / --file, or (3) StdinContent (pipe, here-string, heredoc, file redirect)
	insertText := opts.EditText
	hasInsertText := opts.HasEditText
	useDriveConvert := false
	convertMime := ""
	var convertBytes []byte
	isMarkdown := opts.Markdown
	effectiveWorkDir := opts.WorkDir

	if !hasInsertText && len(fileInputs) > 0 {
		bundle, err := workspace.ResolveBundle(workspace.InputConfig{
			RepoURL:  opts.RepoURL,
			Paths:    fileInputs,
			AutoDeps: false,
			WorkDir:  opts.WorkDir,
		})
		if err != nil {
			return nil, err
		}
		if len(bundle.TargetFiles) == 0 {
			return nil, fmt.Errorf("no readable file content found in %v", fileInputs)
		}
		if (opts.CreateDoc || docID == "") && bundle.UseDriveConversion && len(bundle.ConversionBytes) > 0 {
			useDriveConvert = true
			convertMime = bundle.ConversionMimeType
			convertBytes = bundle.ConversionBytes
		}
		// Auto-detect Markdown conversion when a .md / .markdown file is passed to edit an existing Google Doc
		// (unless --no-markdown / --raw-text was explicitly passed).
		if opts.ExplicitMD == nil {
			allMD := len(bundle.TargetFiles) > 0
			for _, tf := range bundle.TargetFiles {
				ext := strings.ToLower(filepath.Ext(tf.Path))
				if tf.MimeType != workspace.MimeTypeMarkdown && ext != ".md" && ext != ".markdown" {
					allMD = false
					break
				}
			}
			if allMD {
				isMarkdown = true
			}
		}
		if len(bundle.TargetFiles) == 1 && bundle.TargetFiles[0].Content != "" {
			insertText = bundle.TargetFiles[0].Content
			if bundle.TargetFiles[0].Path != "stdin" && !workspace.IsHTTPURL(bundle.TargetFiles[0].Path) {
				mdDir := filepath.Dir(bundle.TargetFiles[0].Path)
				if effectiveWorkDir == "" {
					effectiveWorkDir = mdDir
				} else if effectiveWorkDir != mdDir {
					effectiveWorkDir = mdDir + string(os.PathListSeparator) + effectiveWorkDir
				}
			}
		} else {
			var parts []string
			for _, tf := range bundle.TargetFiles {
				if tf.Content != "" {
					parts = append(parts, tf.Content)
				}
			}
			insertText = strings.Join(parts, "\n")
			if insertText == "" {
				insertText = bundle.CombinedDocText
			}
		}
		hasInsertText = insertText != "" || useDriveConvert
	}

	if !hasInsertText && opts.StdinContent != "" {
		// Strip at most one trailing newline added automatically by shell pipes ('echo'), here-strings ('<<<'), or heredocs ('<<EOF')
		insertText = strings.TrimSuffix(strings.TrimSuffix(opts.StdinContent, "\n"), "\r")
		hasInsertText = true
	}

	// If --replace-range, --replace-body, or --find was passed without explicit --replace value,
	// allow the replacement text to come from --text, -i <file>, or stdin (pipe, here-string, heredoc, redirect)
	replaceText := opts.ReplaceText
	hasReplace := opts.HasReplace
	if !hasReplace && (opts.ReplaceRange != "" || opts.ReplaceBody || opts.FindText != "") && hasInsertText {
		replaceText = insertText
		hasReplace = true
	}

	matchCase := true
	if opts.MatchCase != nil {
		matchCase = *opts.MatchCase
	}

	if isJa {
		spin.Start("Step 1/1: Google ドキュメントの直接編集操作を実行中...")
	} else {
		spin.Start("Step 1/1: Executing direct Google Document edit operation...")
	}

	editRes, err := docsClient.EditDocument(ctx, gdocs.EditDocumentRequest{
		DocID:           docID,
		TabID:           opts.TabID,
		CreateDoc:       opts.CreateDoc,
		DeleteDoc:       opts.DeleteDoc,
		Title:           opts.Title,
		ParentFolder:    opts.ParentFolder,
		UseDriveConvert: useDriveConvert,
		ConvertMimeType: convertMime,
		ConvertBytes:    convertBytes,
		WorkDir:         effectiveWorkDir,
		InsertText:      insertText,
		HasInsertText:   hasInsertText,
		Markdown:        isMarkdown,
		Position:        opts.Position,
		AtSpec:          opts.AtSpec,
		BeforeText:      opts.BeforeText,
		AfterText:       opts.AfterText,
		BeforeParagraph: opts.BeforeParagraph,
		AfterParagraph:  opts.AfterParagraph,
		ParagraphStyle:  opts.ParagraphStyle,
		FindText:        opts.FindText,
		ReplaceText:     replaceText,
		HasReplace:      hasReplace,
		ReplaceRange:    opts.ReplaceRange,
		ReplaceBody:     opts.ReplaceBody,
		MatchCase:       matchCase,
		Occurrence:      opts.Occurrence,
		DeleteRange:     opts.DeleteRange,
		DeleteText:      opts.DeleteText,
		ClearBody:       opts.ClearBody,
		InsertImage:     opts.InsertImage,
		ReplaceImage:    opts.ReplaceImage,
		ResizeImage:     opts.ResizeImage,
		WithImage:       opts.WithImage,
		DeleteImage:     opts.DeleteImage,
		ImageWidth:      opts.ImageWidth,
		ImageHeight:     opts.ImageHeight,
		RangeSpec:       opts.RangeSpec,
		SuggestMode:     opts.EditSuggest,
	}, isJa)
	if err != nil {
		return nil, err
	}

	spin.Stop("Step 1/1: " + editRes.SummaryMessage)

	return &CLIOutput{
		Success:            true,
		Command:            "edit",
		Model:              resolvedModel,
		Mode:               opts.Mode,
		HintOnly:           hintOnly,
		Language:           opts.Lang,
		DocumentID:         editRes.DocumentID,
		DocumentURL:        editRes.DocumentURL,
		DocumentTitle:      editRes.DocumentTitle,
		ActiveTabID:        editRes.ActiveTabID,
		CreatedNewDocument: editRes.CreatedNewDocument,
		ManageAction:       strings.Join(editRes.Operations, "+"),
		ManagedCount:       editRes.AffectedCount,
		EditResult:         editRes,
	}, nil
}

func executeManageCommand(
	ctx context.Context,
	opts *Options,
	docsClient *gdocs.Client,
	spin *Spinner,
	resolvedModel string,
	hintOnly bool,
	isJa bool,
) (*CLIOutput, error) {
	docID := gdocs.ExtractDocumentID(opts.DocID)
	if docID == "" && len(opts.Inputs) > 0 {
		docID = gdocs.ExtractDocumentID(opts.Inputs[0])
	}

	var actions []string
	totalAffected := 0
	var updatedDR *gdocs.DecisionRecordSummary
	createdNew := false
	title := strings.TrimSpace(opts.Title)

	if opts.CreateDoc {
		if title == "" {
			title = "Untitled Google Document"
		}
		if isJa {
			spin.Start(fmt.Sprintf("新規 Google ドキュメント %q を作成しています...", title))
		} else {
			spin.Start(fmt.Sprintf("Creating new Google Document %q...", title))
		}
		newID, err := docsClient.CreateDocument(ctx, title, opts.ParentFolder)
		if err != nil {
			return nil, err
		}
		docID = newID
		createdNew = true
		actions = append(actions, "create-doc")
		totalAffected++
		if isJa {
			spin.Stop(fmt.Sprintf("新規 Google ドキュメント %q (%s) を作成しました", title, docID))
		} else {
			spin.Stop(fmt.Sprintf("Created new Google Document %q (%s)", title, docID))
		}
	}

	if docID == "" {
		return nil, errors.New("manage command requires --doc-id (-d) with a Google Document ID or URL (or --create-doc to create a new document)")
	}

	if opts.AcceptAll {
		if isJa {
			spin.Start("未解決のインライン提案を一括承認しています...")
		} else {
			spin.Start("Accepting all pending inline suggestions...")
		}
		cnt, dr, err := docsClient.AcceptAllSuggestions(ctx, docID, opts.TabID)
		if err != nil {
			return nil, err
		}
		updatedDR = dr
		actions = append(actions, "accept-all")
		totalAffected += cnt
		if isJa {
			spin.Stop(fmt.Sprintf("%d件のインライン提案を承認しました", cnt))
		} else {
			spin.Stop(fmt.Sprintf("Accepted %d inline suggestion(s)", cnt))
		}
	}

	if opts.RejectAll {
		if isJa {
			spin.Start("未解決のインライン提案を一括却下（クリア）しています...")
		} else {
			spin.Start("Rejecting (clearing) all pending inline suggestions...")
		}
		cnt, dr, err := docsClient.RejectAllSuggestions(ctx, docID, opts.TabID)
		if err != nil {
			return nil, err
		}
		updatedDR = dr
		actions = append(actions, "reject-all")
		totalAffected += cnt
		if isJa {
			spin.Stop(fmt.Sprintf("%d件のインライン提案を却下しました", cnt))
		} else {
			spin.Stop(fmt.Sprintf("Rejected %d inline suggestion(s)", cnt))
		}
	}

	if opts.DeleteComments {
		if isJa {
			spin.Start("ドキュメント上の未解決コメントを一括削除しています...")
		} else {
			spin.Start("Deleting active comments on the document...")
		}
		cnt, err := docsClient.DeleteAllComments(ctx, docID, opts.TabID)
		if err != nil {
			return nil, err
		}
		actions = append(actions, "delete-comments")
		totalAffected += cnt
		if isJa {
			spin.Stop(fmt.Sprintf("%d件のコメントスレッドを削除しました", cnt))
		} else {
			spin.Stop(fmt.Sprintf("Deleted %d comment thread(s)", cnt))
		}
	}

	if opts.ResetRecord {
		if isJa {
			spin.Start("AI推敲コーチの採否・編集記録 (Decision Record) をリセットしています...")
		} else {
			spin.Start("Resetting Coach Impact & Decision Record...")
		}
		updatedDR = docsClient.ResetDocumentDecisionRecord(ctx, docID, opts.TabID)
		actions = append(actions, "reset-record")
		if isJa {
			spin.Stop("AI推敲コーチの採否・編集記録をリセットしました")
		} else {
			spin.Stop("Reset Coach Impact & Decision Record")
		}
	}

	if opts.DeleteDoc {
		if isJa {
			spin.Start("Google ドキュメントを Google Drive から削除しています...")
		} else {
			spin.Start("Deleting Google Document from Google Drive...")
		}
		if err := docsClient.DeleteDocument(ctx, docID); err != nil {
			return nil, err
		}
		updatedDR = gdocs.PurgeDecisionRecordFile(docID)
		actions = append(actions, "delete-doc")
		totalAffected++
		if isJa {
			spin.Stop(fmt.Sprintf("Google ドキュメント (%s) を Google Drive から削除しました", docID))
		} else {
			spin.Stop(fmt.Sprintf("Deleted Google Document %s from Google Drive", docID))
		}
	}

	if len(actions) == 0 {
		return nil, errors.New("manage command requires at least one action flag: --create-doc, --accept-all, --reject-all, --delete-comments, --reset-record, or --delete-doc")
	}
	if updatedDR == nil {
		updatedDR = gdocs.LoadDecisionRecordSummary(docID)
	}

	return &CLIOutput{
		Success:            true,
		Command:            "manage",
		Model:              resolvedModel,
		Mode:               opts.Mode,
		HintOnly:           hintOnly,
		Language:           opts.Lang,
		DocumentID:         docID,
		DocumentURL:        gdocs.DocumentURL(docID),
		DocumentTitle:      title,
		ActiveTabID:        opts.TabID,
		CreatedNewDocument: createdNew,
		ManageAction:       strings.Join(actions, "+"),
		ManagedCount:       totalAffected,
		DecisionRecord:     updatedDR,
	}, nil
}

func executeInspectCommand(
	ctx context.Context,
	opts *Options,
	docsClient *gdocs.Client,
	spin *Spinner,
	resolvedModel string,
	hintOnly bool,
	isJa bool,
) (*CLIOutput, error) {
	docID := gdocs.ExtractDocumentID(opts.DocID)
	if docID == "" && len(opts.Inputs) > 0 {
		docID = gdocs.ExtractDocumentID(opts.Inputs[0])
	}
	if docID == "" {
		return nil, errors.New("inspect command requires --doc-id (-d) with a Google Document ID or URL")
	}

	if isJa {
		spin.Start("Google ドキュメントの段落構成・UTF-16 座標・レビュースレッドを検査しています...")
	} else {
		spin.Start("Inspecting Google Document paragraphs, UTF-16 ranges, and review threads...")
	}
	rawDocJSON, err := docsClient.FetchRawDocumentJSON(ctx, docID)
	if err != nil {
		return nil, err
	}
	docData := gdocs.ParseDocumentStructure(rawDocJSON, gdocs.ParseOptions{
		PreferredTabID: opts.TabID,
		SelectionOnly:  false,
	})
	for i := range docData.EditableBlocks {
		docData.EditableBlocks[i].LineNumber = i + 1
	}
	if isJa {
		spin.Stop(fmt.Sprintf("検査完了 %q: 段落/ブロック %d件, 未解決提案 %d件, コメント %d件, 解決済み履歴 %d件",
			docData.Title, len(docData.EditableBlocks), len(docData.ExistingSuggestions), len(docData.ExistingComments), len(docData.ResolvedReviews)))
	} else {
		spin.Stop(fmt.Sprintf("Inspected %q: %d paragraph(s)/block(s), %d pending suggestion(s), %d comment(s), %d resolved review(s)",
			docData.Title, len(docData.EditableBlocks), len(docData.ExistingSuggestions), len(docData.ExistingComments), len(docData.ResolvedReviews)))
	}

	return &CLIOutput{
		Success:        true,
		Command:        "inspect",
		Model:          resolvedModel,
		Mode:           opts.Mode,
		HintOnly:       hintOnly,
		Language:       opts.Lang,
		DocumentID:     docID,
		DocumentURL:    gdocs.DocumentURL(docID),
		DocumentTitle:  docData.Title,
		ActiveTabID:    docData.ActiveTabID,
		InspectedDoc:   docData,
		DecisionRecord: docData.DecisionRecord,
	}, nil
}

// resolveExistingDocRange resolves --range on an existing or converted Google Document, supporting:
// 1. UTF-16 character offset range: "@100:600" or "100:600"
// 2. 1-based paragraph / line number range: "2-5" or "L2-L5"
// 3. Heading or text phrase search: "--range \"Introduction\"" (matches paragraphs containing the phrase)
func resolveExistingDocRange(rawDocJSON map[string]any, tabID, rangeSpec string) ([]utf16idx.RangeInterval, bool, error) {
	trimmed := strings.TrimSpace(rangeSpec)
	if trimmed == "" {
		return nil, false, nil
	}

	if tf, err := workspace.ParseStandaloneRange(trimmed); err == nil {
		if tf.StartChar >= 0 && tf.EndChar > tf.StartChar {
			return []utf16idx.RangeInterval{{StartIndex: maxInt(1, tf.StartChar), EndIndex: tf.EndChar}}, true, nil
		}
		if tf.StartLine > 0 {
			fullDoc := gdocs.ParseDocumentStructure(rawDocJSON, gdocs.ParseOptions{
				PreferredTabID:        tabID,
				SelectionOnly:         false,
				SkipDecisionReconcile: true,
			})
			if len(fullDoc.EditableBlocks) == 0 {
				return nil, false, errors.New("document has no text paragraphs to match line range")
			}
			sIdx := max(tf.StartLine-1, 0)
			eIdx := tf.EndLine - 1
			if eIdx < 0 || eIdx >= len(fullDoc.EditableBlocks) {
				eIdx = len(fullDoc.EditableBlocks) - 1
			}
			if sIdx >= len(fullDoc.EditableBlocks) || sIdx > eIdx {
				return nil, false, fmt.Errorf("paragraph/line range %q is out of bounds (document has %d paragraphs; run 'gdocumentor inspect -d <doc>' to view)", trimmed, len(fullDoc.EditableBlocks))
			}
			return []utf16idx.RangeInterval{{
				StartIndex: fullDoc.EditableBlocks[sIdx].StartIndex,
				EndIndex:   fullDoc.EditableBlocks[eIdx].EndIndex,
			}}, true, nil
		}
	}

	// Fallback: treat trimmed as a heading / text substring query across document paragraphs
	fullDoc := gdocs.ParseDocumentStructure(rawDocJSON, gdocs.ParseOptions{
		PreferredTabID:        tabID,
		SelectionOnly:         false,
		SkipDecisionReconcile: true,
	})
	needle := strings.ToLower(trimmed)
	var intervals []utf16idx.RangeInterval
	for _, blk := range fullDoc.EditableBlocks {
		if strings.Contains(strings.ToLower(blk.Text), needle) {
			intervals = append(intervals, utf16idx.RangeInterval{
				StartIndex: blk.StartIndex,
				EndIndex:   blk.EndIndex,
			})
		}
	}
	if len(intervals) > 0 {
		return intervals, true, nil
	}
	return nil, false, fmt.Errorf("invalid or unmatched --range %q (pass '@start:end' UTF-16 offsets, 'start-end' paragraph numbers, or a matching text phrase; run 'gdocumentor inspect -d <doc>' to inspect)", rangeSpec)
}

func executeDraftCommand(
	ctx context.Context,
	opts *Options,
	docsClient *gdocs.Client,
	geminiClient *gemini.Client,
	spin *Spinner,
	resolvedModel string,
	hintOnly bool,
	isJa bool,
) (*CLIOutput, error) {
	if isJa {
		spin.Start("Step 1/3: 参照コンテキストと RAG ルールブックを並列読み込み中...")
	} else {
		spin.Start("Step 1/3: Resolving context files and RAG guidelines in parallel...")
	}
	docID := gdocs.ExtractDocumentID(opts.DocID)

	var (
		bundle     *workspace.Bundle
		bundleErr  error
		driveRules []workspace.RagRuleFile
		driveErr   error
		wg         sync.WaitGroup
	)

	wg.Go(func() {
		bundle, bundleErr = workspace.ResolveBundle(workspace.InputConfig{
			RepoURL:      opts.RepoURL,
			ContextPaths: opts.Contexts,
			RulePaths:    opts.Rules,
			RulesDir:     opts.RulesDir,
			WorkDir:      opts.WorkDir,
		})
	})

	if opts.DriveRAG != "" {
		wg.Go(func() {
			driveRules, driveErr = docsClient.FetchRagRuleFilesFromDrive(ctx, opts.DriveRAG, docID)
		})
	}

	wg.Wait()
	if bundleErr != nil {
		return nil, bundleErr
	}
	if driveErr != nil {
		return nil, driveErr
	}
	bundle.RagRules = append(bundle.RagRules, driveRules...)
	activePersona := gemini.ResolveEffectivePersona(opts.Persona, bundle.RagRules)

	if strings.TrimSpace(opts.GeneratedDraft) == "" && opts.HasEditText && strings.TrimSpace(opts.EditText) != "" {
		opts.GeneratedDraft = opts.EditText
	}

	var draftText string
	if strings.TrimSpace(opts.GeneratedDraft) != "" {
		draftText = strings.TrimSpace(opts.GeneratedDraft)
		if isJa {
			spin.Stop(fmt.Sprintf("Step 1/3: 参照ファイル %d件、RAG ルールブック %d件を読み込みました", len(bundle.ReferenceFiles), len(bundle.RagRules)))
			spin.Stop(fmt.Sprintf("Step 2/3: エージェントAIが生成した初期ドラフトを受領しました (%d 文字)", len([]rune(draftText))))
		} else {
			spin.Stop(fmt.Sprintf("Step 1/3: Loaded %d reference file(s) and %d RAG rulebook(s)", len(bundle.ReferenceFiles), len(bundle.RagRules)))
			spin.Stop(fmt.Sprintf("Step 2/3: Received Host-AI generated initial draft (%d chars)", len(draftText)))
		}
	} else if opts.PrepareOnly || opts.UseAgentAI || geminiClient == nil {
		if isJa {
			spin.Stop(fmt.Sprintf("Step 1/3: 参照ファイル %d件、RAG ルールブック %d件を読み込みました", len(bundle.ReferenceFiles), len(bundle.RagRules)))
		} else {
			spin.Stop(fmt.Sprintf("Step 1/3: Loaded %d reference file(s) and %d RAG rulebook(s)", len(bundle.ReferenceFiles), len(bundle.RagRules)))
		}
		agentPrompt := gemini.BuildAgentDraftPrompt(gemini.DraftPromptOptions{
			Persona:        opts.Persona,
			Lang:           opts.Lang,
			CustomPrompt:   opts.Prompt,
			ReferenceFiles: bundle.ReferenceFiles,
			RagRules:       bundle.RagRules,
		})
		return &CLIOutput{
			Success:               true,
			Command:               "draft",
			Model:                 resolvedModel,
			Mode:                  opts.Mode,
			HintOnly:              hintOnly,
			Language:              opts.Lang,
			LocalMode:             opts.NoAuth,
			RequiresAgentAI:       true,
			DocumentID:            docID,
			DocumentURL:           gdocs.DocumentURL(docID),
			DocumentTitle:         opts.Title,
			ReferenceContextFiles: extractFilePaths(bundle.ReferenceFiles),
			RagRuleFiles:          extractRuleTitles(bundle.RagRules),
			ActivePersona:         activePersona,
			PreparedContext: &AgentPreparedContext{
				Phase:                "phase1_prepared_for_agent_ai",
				NextStepInstructions: "Generate the initial draft text yourself using your own LLM following coachingPrompt, then call gdocumentor_draft again with generatedDraft set to your generated text (plus the same title, docId, workDir, outputPath, or noAuth parameters) to insert it into Google Docs or save it locally.",
				DocumentID:           docID,
				DocumentURL:          gdocs.DocumentURL(docID),
				CoachingPrompt:       agentPrompt,
			},
		}, nil
	} else {
		if isJa {
			spin.Stop(fmt.Sprintf("Step 1/3: 参照ファイル %d件、RAG ルールブック %d件を読み込みました", len(bundle.ReferenceFiles), len(bundle.RagRules)))
			spin.Start(fmt.Sprintf("Step 2/3: %s で初期ドラフト（初稿）を生成中...", resolvedModel))
		} else {
			spin.Stop(fmt.Sprintf("Step 1/3: Loaded %d reference file(s) and %d RAG rulebook(s)", len(bundle.ReferenceFiles), len(bundle.RagRules)))
			spin.Start(fmt.Sprintf("Step 2/3: Generating initial draft with %s...", resolvedModel))
		}
		var err error
		draftText, err = geminiClient.GenerateInitialDraft(ctx, gemini.DraftPromptOptions{
			Persona:        opts.Persona,
			Lang:           opts.Lang,
			CustomPrompt:   opts.Prompt,
			ReferenceFiles: bundle.ReferenceFiles,
			RagRules:       bundle.RagRules,
		})
		if err != nil {
			return nil, err
		}
		if isJa {
			spin.Stop(fmt.Sprintf("Step 2/3: 初期ドラフトを生成しました (%d 文字)", len([]rune(draftText))))
		} else {
			spin.Stop(fmt.Sprintf("Step 2/3: Generated initial draft (%d chars)", len(draftText)))
		}
	}

	createdNew := false
	title := strings.TrimSpace(opts.Title)
	if title == "" {
		title = fmt.Sprintf("DocuMentor AI Draft (%s)", time.Now().Format("2006-01-02 15:04"))
	}

	localOutPath := resolveLocalOutputPath(opts, bundle, docID, true, false, hintOnly)

	if opts.NoAuth {
		if isJa {
			spin.Start(fmt.Sprintf("Step 3/3: 生成された初期ドラフトをローカルファイル %q に保存中...", localOutPath))
		} else {
			spin.Start(fmt.Sprintf("Step 3/3: Writing generated initial draft to local file %q...", localOutPath))
		}
		out := &CLIOutput{
			Success:               true,
			Command:               "draft",
			Model:                 resolvedModel,
			Mode:                  opts.Mode,
			HintOnly:              hintOnly,
			Language:              opts.Lang,
			LocalMode:             true,
			OutputFilePath:        localOutPath,
			DocumentTitle:         title,
			ReferenceContextFiles: extractFilePaths(bundle.ReferenceFiles),
			RagRuleFiles:          extractRuleTitles(bundle.RagRules),
			ActivePersona:         activePersona,
			GeneratedDraft:        draftText,
			OverallFeedback: map[bool]string{
				true:  fmt.Sprintf("初期ドラフトをローカルファイル `%s` に出力しました。続けて `gdocumentor --no-auth -i %s` を実行して推敲を行えます。", localOutPath, localOutPath),
				false: fmt.Sprintf("Saved initial draft to local file `%s`. You can now run `gdocumentor --no-auth -i %s` to proofread it.", localOutPath, localOutPath),
			}[isJa],
		}
		if err := writeLocalDraftFile(localOutPath, out); err != nil {
			return nil, err
		}
		if isJa {
			spin.Stop(fmt.Sprintf("Step 3/3: 初期ドラフトを %s に保存しました", localOutPath))
		} else {
			spin.Stop(fmt.Sprintf("Step 3/3: Saved initial draft to %s", localOutPath))
		}
		return out, nil
	}

	if docID == "" {
		if isJa {
			spin.Start("Step 3/3: 新規 Google ドキュメントを作成して初期ドラフトを挿入中...")
		} else {
			spin.Start("Step 3/3: Creating new Google Document and inserting initial draft...")
		}
		newID, cErr := docsClient.CreateDocument(ctx, title, opts.ParentFolder)
		if cErr != nil {
			return nil, cErr
		}
		docID = newID
		createdNew = true
	} else {
		if isJa {
			spin.Start("Step 3/3: Google ドキュメントへ初期ドラフトを挿入中...")
		} else {
			spin.Start("Step 3/3: Inserting initial draft into Google Document...")
		}
	}

	if err := docsClient.InsertInitialText(ctx, docID, opts.TabID, draftText); err != nil {
		return nil, err
	}
	if isJa {
		spin.Stop("Step 3/3: Google ドキュメントへの初期ドラフト挿入が完了しました")
	} else {
		spin.Stop("Step 3/3: Initial draft inserted into Google Document")
	}

	out := &CLIOutput{
		Success:               true,
		Command:               "draft",
		Model:                 resolvedModel,
		Mode:                  opts.Mode,
		HintOnly:              hintOnly,
		Language:              opts.Lang,
		OutputFilePath:        localOutPath,
		DocumentID:            docID,
		DocumentURL:           gdocs.DocumentURL(docID),
		DocumentTitle:         title,
		ActiveTabID:           opts.TabID,
		CreatedNewDocument:    createdNew,
		ReferenceContextFiles: extractFilePaths(bundle.ReferenceFiles),
		RagRuleFiles:          extractRuleTitles(bundle.RagRules),
		ActivePersona:         activePersona,
		GeneratedDraft:        draftText,
		OverallFeedback: map[bool]string{
			true:  "初期ドラフトをGoogleドキュメントに挿入しました。続けて `gdocumentor -d " + docID + "` を実行して推敲を行えます。",
			false: "Inserted initial draft into Google Docs. You can now run `gdocumentor -d " + docID + "` to proofread it.",
		}[isJa],
	}
	if localOutPath != "" {
		if err := writeLocalDraftFile(localOutPath, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func executeSummaryCommand(
	ctx context.Context,
	opts *Options,
	docsClient *gdocs.Client,
	geminiClient *gemini.Client,
	spin *Spinner,
	resolvedModel string,
	hintOnly bool,
	isJa bool,
) (*CLIOutput, error) {
	docID := gdocs.ExtractDocumentID(opts.DocID)
	if docID == "" && len(opts.Inputs) > 0 {
		docID = gdocs.ExtractDocumentID(opts.Inputs[0])
	}
	if docID == "" {
		return nil, errors.New("summary command requires --doc-id (-d) with a Google Document ID or URL")
	}

	if isJa {
		spin.Start("Step 1/2: Google ドキュメントの構造・提案・コメントおよび RAG ルールを並列取得中...")
	} else {
		spin.Start("Step 1/2: Fetching Google Document structure, suggestions, comments, and RAG rules in parallel...")
	}

	var (
		rawDocJSON map[string]any
		docErr     error
		bundle     *workspace.Bundle
		bundleErr  error
		driveRules []workspace.RagRuleFile
		driveErr   error
		wg         sync.WaitGroup
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		rawDocJSON, docErr = docsClient.FetchRawDocumentJSON(ctx, docID)
	}()
	go func() {
		defer wg.Done()
		bundle, bundleErr = workspace.ResolveBundle(workspace.InputConfig{
			RepoURL:      opts.RepoURL,
			ContextPaths: opts.Contexts,
			RulePaths:    opts.Rules,
			RulesDir:     opts.RulesDir,
			WorkDir:      opts.WorkDir,
		})
	}()
	if opts.DriveRAG != "" {
		wg.Go(func() {
			driveRules, driveErr = docsClient.FetchRagRuleFilesFromDrive(ctx, opts.DriveRAG, docID)
		})
	}

	wg.Wait()
	if docErr != nil {
		return nil, docErr
	}
	if bundleErr != nil {
		return nil, bundleErr
	}
	if driveErr != nil {
		return nil, driveErr
	}
	bundle.RagRules = append(bundle.RagRules, driveRules...)
	activePersona := gemini.ResolveEffectivePersona(opts.Persona, bundle.RagRules)

	selRanges, selectionOnly, rErr := resolveExistingDocRange(rawDocJSON, opts.TabID, opts.RangeSpec)
	if rErr != nil {
		return nil, rErr
	}

	docData := gdocs.ParseDocumentStructure(rawDocJSON, gdocs.ParseOptions{
		PreferredTabID:  opts.TabID,
		SelectionOnly:   selectionOnly,
		SelectionRanges: selRanges,
	})

	visualsWithImages := docsClient.FetchVisualImages(ctx, docData.VisualElements)

	if opts.AISummaryResult == nil && opts.AIResult != nil {
		opts.AISummaryResult = &gemini.SummaryResult{
			OverallFeedback: opts.AIResult.OverallFeedback,
			LearningSummary: opts.AIResult.LearningSummary,
		}
	}

	totalReviewItems := len(docData.ExistingSuggestions) + len(docData.ExistingComments)
	if opts.IncludeResolved {
		totalReviewItems += len(docData.ResolvedReviews)
	}
	if totalReviewItems == 0 && opts.AISummaryResult == nil {
		if isJa {
			return nil, errors.New("対象ドキュメント（または選択範囲）に分析可能な提案やコメントが見つかりません")
		}
		return nil, errors.New("no existing suggestions or comments found in the target document scope to summarize")
	}

	var sumRes *gemini.SummaryResult
	if opts.AISummaryResult != nil {
		sumRes = opts.AISummaryResult
		if isJa {
			spin.Stop(fmt.Sprintf("Step 1/2: 未解決提案 %d件、アクティブコメント %d件、解決済み履歴 %d件を検出しました",
				len(docData.ExistingSuggestions), len(docData.ExistingComments), len(docData.ResolvedReviews)))
			spin.Stop(fmt.Sprintf("Step 2/2: エージェントAIが生成した学習サマリーを受領しました (%d 項目)", len(sumRes.AnalyzedItems)))
		} else {
			spin.Stop(fmt.Sprintf("Step 1/2: Found %d pending suggestion(s), %d active comment(s), %d resolved review(s)",
				len(docData.ExistingSuggestions), len(docData.ExistingComments), len(docData.ResolvedReviews)))
			spin.Stop(fmt.Sprintf("Step 2/2: Received Host-AI generated learning summary (%d item(s))", len(sumRes.AnalyzedItems)))
		}
	} else if opts.PrepareOnly || opts.UseAgentAI || geminiClient == nil {
		if isJa {
			spin.Stop(fmt.Sprintf("Step 1/2: 未解決提案 %d件、アクティブコメント %d件、解決済み履歴 %d件を検出しました",
				len(docData.ExistingSuggestions), len(docData.ExistingComments), len(docData.ResolvedReviews)))
		} else {
			spin.Stop(fmt.Sprintf("Step 1/2: Found %d pending suggestion(s), %d active comment(s), %d resolved review(s)",
				len(docData.ExistingSuggestions), len(docData.ExistingComments), len(docData.ResolvedReviews)))
		}
		agentPrompt, schema := gemini.BuildAgentSummaryPrompt(gemini.SummaryPromptOptions{
			DocData:           docData,
			ReferenceFiles:    bundle.ReferenceFiles,
			RagRules:          bundle.RagRules,
			VisualsWithImages: visualsWithImages,
			Persona:           opts.Persona,
			Lang:              opts.Lang,
			CustomPrompt:      opts.Prompt,
			IncludeResolved:   opts.IncludeResolved,
		})
		return &CLIOutput{
			Success:               true,
			Command:               "summary",
			Model:                 resolvedModel,
			Mode:                  opts.Mode,
			HintOnly:              hintOnly,
			Language:              opts.Lang,
			RequiresAgentAI:       true,
			DocumentID:            docID,
			DocumentURL:           gdocs.DocumentURL(docID),
			DocumentTitle:         docData.Title,
			ActiveTabID:           docData.ActiveTabID,
			SelectionOnly:         docData.SelectionOnly,
			ReferenceContextFiles: extractFilePaths(bundle.ReferenceFiles),
			RagRuleFiles:          extractRuleTitles(bundle.RagRules),
			ActivePersona:         activePersona,
			DecisionRecord:        docData.DecisionRecord,
			PreparedContext: &AgentPreparedContext{
				Phase:                "phase1_prepared_for_agent_ai",
				NextStepInstructions: fmt.Sprintf("Synthesize the Writing Coach & Learning Summary JSON yourself using your own LLM following coachingPrompt and expectedJsonSchema, then call gdocumentor_summary again with docId: %q and aiSummaryResult: { overallFeedback, learningSummary, analyzedItems } (plus summaryTab/outputPath/saveLocal if requested).", docID),
				DocumentID:           docID,
				DocumentURL:          gdocs.DocumentURL(docID),
				ActiveTabID:          docData.ActiveTabID,
				ExistingSuggestions:  docData.ExistingSuggestions,
				ExistingComments:     docData.ExistingComments,
				ResolvedReviews:      docData.ResolvedReviews,
				CoachingPrompt:       agentPrompt,
				ExpectedJSONSchema:   schema,
			},
		}, nil
	} else {
		if isJa {
			spin.Stop(fmt.Sprintf("Step 1/2: 未解決提案 %d件、アクティブコメント %d件、解決済み履歴 %d件を検出しました",
				len(docData.ExistingSuggestions), len(docData.ExistingComments), len(docData.ResolvedReviews)))
			spin.Start(fmt.Sprintf("Step 2/2: %s でライティングコーチ＆学習サマリーを生成中...", resolvedModel))
		} else {
			spin.Stop(fmt.Sprintf("Step 1/2: Found %d pending suggestion(s), %d active comment(s), %d resolved review(s)",
				len(docData.ExistingSuggestions), len(docData.ExistingComments), len(docData.ResolvedReviews)))
			spin.Start(fmt.Sprintf("Step 2/2: Synthesizing Writing Coach & Learning Summary with %s...", resolvedModel))
		}
		var err error
		sumRes, err = geminiClient.GenerateLearningSummaryFromExisting(ctx, gemini.SummaryPromptOptions{
			DocData:           docData,
			ReferenceFiles:    bundle.ReferenceFiles,
			RagRules:          bundle.RagRules,
			VisualsWithImages: visualsWithImages,
			Persona:           opts.Persona,
			Lang:              opts.Lang,
			CustomPrompt:      opts.Prompt,
			IncludeResolved:   opts.IncludeResolved,
		})
		if err != nil {
			return nil, err
		}
		if isJa {
			spin.Stop(fmt.Sprintf("Step 2/2: %d件のレビュー項目から学習サマリーを生成しました", len(sumRes.AnalyzedItems)))
		} else {
			spin.Stop(fmt.Sprintf("Step 2/2: Synthesized learning summary across %d review item(s)", len(sumRes.AnalyzedItems)))
		}
	}

	localOutPath := resolveLocalOutputPath(opts, bundle, docID, false, true, hintOnly)

	out := &CLIOutput{
		Success:               true,
		Command:               "summary",
		Model:                 resolvedModel,
		Mode:                  opts.Mode,
		HintOnly:              hintOnly,
		Language:              opts.Lang,
		OutputFilePath:        localOutPath,
		DocumentID:            docID,
		DocumentURL:           gdocs.DocumentURL(docID),
		DocumentTitle:         docData.Title,
		ActiveTabID:           docData.ActiveTabID,
		SelectionOnly:         docData.SelectionOnly,
		ReferenceContextFiles: extractFilePaths(bundle.ReferenceFiles),
		RagRuleFiles:          extractRuleTitles(bundle.RagRules),
		ActivePersona:         activePersona,
		OverallFeedback:       sumRes.OverallFeedback,
		LearningSummary:       &sumRes.LearningSummary,
		AnalyzedItems:         sumRes.AnalyzedItems,
		DecisionRecord:        docData.DecisionRecord,
	}
	if opts.SummaryTab {
		if isJa {
			spin.Start("Google ドキュメント内のまとめ専用タブを作成・更新中...")
		} else {
			spin.Start("Updating Google Docs Coaching Summary Tab...")
		}
		summaryContent, summaryHeadings := buildGoogleDocsSummaryTabContent(out, isJa)
		tabRes, tabErr := docsClient.UpsertDocumentSummaryTab(ctx, docID, opts.SummaryTabName, summaryContent, summaryHeadings)
		if tabErr != nil {
			return nil, tabErr
		}
		out.SummaryTabID = tabRes.TabID
		out.SummaryTabTitle = tabRes.TabTitle
		out.SummaryTabURL = tabRes.TabURL
		out.SummaryTabCreatedNew = tabRes.CreatedNewTab
		if isJa {
			spin.Stop(fmt.Sprintf("Google ドキュメント・まとめタブ %q (%s) を更新しました", tabRes.TabTitle, tabRes.TabID))
		} else {
			spin.Stop(fmt.Sprintf("Updated Google Docs Summary Tab %q (%s)", tabRes.TabTitle, tabRes.TabID))
		}
	}
	if localOutPath != "" {
		if err := writeLocalSummaryReport(localOutPath, out, isJa); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func executeProofreadCommand(
	ctx context.Context,
	opts *Options,
	docsClient *gdocs.Client,
	geminiClient *gemini.Client,
	spin *Spinner,
	resolvedModel string,
	hintOnly bool,
	isJa bool,
) (*CLIOutput, error) {
	if isJa {
		spin.Start("Step 1/4: 対象ファイル・依存関係・RAG ルール・Google ドキュメントを並列解決中...")
	} else {
		spin.Start("Step 1/4: Resolving workspace files, dependencies, RAG rules, and Google Document in parallel...")
	}

	docID := gdocs.ExtractDocumentID(opts.DocID)
	hasLocalInputs := len(opts.Inputs) > 0 || len(opts.Targets) > 0 || strings.TrimSpace(opts.StdinContent) != ""
	localRangeSpec := opts.RangeSpec
	if !hasLocalInputs {
		localRangeSpec = ""
	}

	var (
		bundle         *workspace.Bundle
		bundleErr      error
		driveRules     []workspace.RagRuleFile
		driveErr       error
		existingRawDoc map[string]any
		existingDocErr error
		step1Wg        sync.WaitGroup
	)

	step1Wg.Go(func() {
		bundle, bundleErr = workspace.ResolveBundle(workspace.InputConfig{
			RepoURL:      opts.RepoURL,
			Paths:        opts.Inputs,
			Targets:      opts.Targets,
			RangeSpec:    localRangeSpec,
			ContextPaths: opts.Contexts,
			AutoDeps:     opts.AutoDeps,
			RulePaths:    opts.Rules,
			RulesDir:     opts.RulesDir,
			StdinContent: opts.StdinContent,
			WorkDir:      opts.WorkDir,
		})
	})

	if opts.DriveRAG != "" {
		step1Wg.Go(func() {
			driveRules, driveErr = docsClient.FetchRagRuleFilesFromDrive(ctx, opts.DriveRAG, docID)
		})
	}

	// If reading an existing Google Document (-d) with no local target files, fetch the Google Doc in parallel too
	if !hasLocalInputs && docID != "" {
		step1Wg.Go(func() {
			existingRawDoc, existingDocErr = docsClient.FetchRawDocumentJSON(ctx, docID)
		})
	}

	step1Wg.Wait()
	if bundleErr != nil {
		return nil, bundleErr
	}
	if driveErr != nil {
		return nil, driveErr
	}
	bundle.RagRules = append(bundle.RagRules, driveRules...)
	activePersona := gemini.ResolveEffectivePersona(opts.Persona, bundle.RagRules)

	createdNewDoc := false
	usedDriveConversion := false

	if len(bundle.TargetFiles) == 0 && docID == "" {
		return nil, errors.New("no target input files or --doc-id specified (pass -i <file/dir>, -t <file:lines>, -d <docId>, or pipe stdin)")
	}

	var (
		docData           *gdocs.ParsedDocument
		visualsWithImages []gdocs.VisualElement
	)

	if opts.NoAuth {
		for _, tf := range bundle.TargetFiles {
			if tf.MimeType == workspace.MimeTypePDF && strings.TrimSpace(tf.Content) == "" {
				return nil, fmt.Errorf("target file %q is a binary PDF, which requires Google Drive API conversion (omit --no-auth to convert PDF to Google Docs, or pass PDF as -r rulebook in --no-auth mode)", tf.DisplayPath)
			}
		}
		title := strings.TrimSpace(opts.Title)
		if title == "" {
			if len(bundle.TargetFiles) == 1 {
				title = fmt.Sprintf("DocuMentor AI - %s (%s)", filepath.Base(bundle.TargetFiles[0].DisplayPath), time.Now().Format("2006-01-02 15:04"))
			} else {
				title = fmt.Sprintf("DocuMentor AI - %d Files Review (%s)", len(bundle.TargetFiles), time.Now().Format("2006-01-02 15:04"))
			}
		}
		docData, visualsWithImages = buildLocalParsedDocument(bundle, title)
		if isJa {
			spin.Stop(fmt.Sprintf("Step 1/4: ローカルモード準備完了 (対象=%d, 参照=%d, RAGルール=%d, ブロック=%d, 図=%d, 表=%d)",
				len(bundle.TargetFiles), len(bundle.ReferenceFiles), len(bundle.RagRules), len(docData.EditableBlocks), len(visualsWithImages), len(docData.Tables)))
		} else {
			spin.Stop(fmt.Sprintf("Step 1/4: Ready in Local Mode (TargetFiles=%d, ContextFiles=%d, RAGRules=%d, EditableBlocks=%d, Visuals=%d, Tables=%d)",
				len(bundle.TargetFiles), len(bundle.ReferenceFiles), len(bundle.RagRules), len(docData.EditableBlocks), len(visualsWithImages), len(docData.Tables)))
		}
	} else if len(bundle.TargetFiles) > 0 {
		title := strings.TrimSpace(opts.Title)
		if title == "" {
			if len(bundle.TargetFiles) == 1 {
				title = fmt.Sprintf("DocuMentor AI - %s (%s)", filepath.Base(bundle.TargetFiles[0].DisplayPath), time.Now().Format("2006-01-02 15:04"))
			} else {
				title = fmt.Sprintf("DocuMentor AI - %d Files Review (%s)", len(bundle.TargetFiles), time.Now().Format("2006-01-02 15:04"))
			}
		}

		// When Phase 2 is called with an existing docId created in Phase 1 (skipDocInsert or aiResult provided),
		// do NOT re-insert initial text if the Google Document already contains content; instead reconcile local scope metadata.
		alreadyInsertedInDoc := false
		if docID != "" && (opts.SkipDocInsert || opts.AIResult != nil) {
			if rawDocJSON, fErr := docsClient.FetchRawDocumentJSON(ctx, docID); fErr == nil {
				prelimParsed := gdocs.ParseDocumentStructure(rawDocJSON, gdocs.ParseOptions{
					PreferredTabID:        opts.TabID,
					SelectionOnly:         false,
					SkipDecisionReconcile: true,
				})
				if !prelimParsed.IsEmpty {
					alreadyInsertedInDoc = true
					docData = gdocs.ParseDocumentStructure(rawDocJSON, gdocs.ParseOptions{
						PreferredTabID: opts.TabID,
						SelectionOnly:  false,
					})
					if bundle.UseDriveConversion {
						usedDriveConversion = true
						reconciledBlocks, selRanges := workspace.ReconcileConvertedBlocksWithLocalScope(
							docData.EditableBlocks,
							bundle.EditableBlocks,
							bundle.TargetFiles,
							bundle.HasPartialScope,
						)
						docData.EditableBlocks = reconciledBlocks
						docData.SelectionOnly = bundle.HasPartialScope
						if len(selRanges) > 0 {
							docData.SelectionRanges = selRanges
						} else {
							docData.SelectionRanges = bundle.DocSelectionRanges
						}
					} else {
						docData.EditableBlocks = workspace.ReconcileFetchedBlocksWithLocalMapping(docData.EditableBlocks, bundle.EditableBlocks)
						docData.SelectionOnly = bundle.HasPartialScope
						docData.SelectionRanges = bundle.DocSelectionRanges
					}
				}
			}
		}

		if !alreadyInsertedInDoc && docID == "" && bundle.UseDriveConversion && len(bundle.ConversionBytes) > 0 {
			// Markdown (.md), Text (.txt), PDF (.pdf), and DOCX (.docx) files are converted directly
			// into native Google Docs via Google Drive API v3 multipart conversion.
			newID, convErr := docsClient.ConvertFileToGoogleDoc(
				ctx,
				title,
				opts.ParentFolder,
				bundle.ConversionMimeType,
				bundle.ConversionBytes,
				bundle.CombinedDocText,
			)
			if convErr != nil {
				return nil, convErr
			}
			docID = newID
			createdNewDoc = true
			usedDriveConversion = true

			rawDocJSON, err := docsClient.FetchRawDocumentJSON(ctx, docID)
			if err != nil {
				return nil, err
			}

			// Fallback: if Markdown had embedded local images and Base64 conversion didn't yield VisualElements,
			// upload images to Drive in parallel, expand thumbnailLink size (=s2048), and insert into the Doc.
			prelimParsed := gdocs.ParseDocumentStructure(rawDocJSON, gdocs.ParseOptions{
				PreferredTabID:        opts.TabID,
				SelectionOnly:         false,
				SkipDecisionReconcile: true,
			})
			if len(bundle.EmbeddedImages) > 0 && len(prelimParsed.VisualElements) == 0 {
				if fbErr := docsClient.InsertImagesViaDriveThumbnailFallback(ctx, docID, prelimParsed.ActiveTabID, bundle.EmbeddedImages); fbErr == nil {
					if refreshedJSON, rErr := docsClient.FetchRawDocumentJSON(ctx, docID); rErr == nil {
						rawDocJSON = refreshedJSON
					}
				}
			}

			// Check if --range was passed for a binary converted document (.pdf or .docx)
			isBinaryConverted := bundle.ConversionMimeType == workspace.MimeTypePDF || bundle.ConversionMimeType == workspace.MimeTypeDOCX
			if isBinaryConverted && strings.TrimSpace(opts.RangeSpec) != "" {
				selRanges, selectionOnly, rErr := resolveExistingDocRange(rawDocJSON, opts.TabID, opts.RangeSpec)
				if rErr != nil {
					return nil, rErr
				}
				docData = gdocs.ParseDocumentStructure(rawDocJSON, gdocs.ParseOptions{
					PreferredTabID:  opts.TabID,
					SelectionOnly:   selectionOnly,
					SelectionRanges: selRanges,
				})
				for i := range docData.EditableBlocks {
					if docData.EditableBlocks[i].FilePath == "" {
						docData.EditableBlocks[i].FilePath = bundle.TargetFiles[0].DisplayPath
					}
					if docData.EditableBlocks[i].LineNumber == 0 {
						docData.EditableBlocks[i].LineNumber = i + 1
					}
				}
			} else {
				docData = gdocs.ParseDocumentStructure(rawDocJSON, gdocs.ParseOptions{
					PreferredTabID: opts.TabID,
					SelectionOnly:  false,
				})
				reconciledBlocks, selRanges := workspace.ReconcileConvertedBlocksWithLocalScope(
					docData.EditableBlocks,
					bundle.EditableBlocks,
					bundle.TargetFiles,
					bundle.HasPartialScope,
				)
				docData.EditableBlocks = reconciledBlocks
				docData.SelectionOnly = bundle.HasPartialScope
				if len(selRanges) > 0 {
					docData.SelectionRanges = selRanges
				} else {
					docData.SelectionRanges = bundle.DocSelectionRanges
				}
			}
		} else if !alreadyInsertedInDoc {
			// Scripts / source code files (.go, .js, .py, .sh, etc.) or insertion into an existing Google Doc:
			// Create a new Google Document and insert the raw source text at index 1.
			if docID == "" {
				newID, cErr := docsClient.CreateDocument(ctx, title, opts.ParentFolder)
				if cErr != nil {
					return nil, cErr
				}
				docID = newID
				createdNewDoc = true
			}

			if err := docsClient.InsertInitialText(ctx, docID, opts.TabID, bundle.CombinedDocText); err != nil {
				return nil, err
			}

			rawDocJSON, err := docsClient.FetchRawDocumentJSON(ctx, docID)
			if err != nil {
				return nil, err
			}
			docData = gdocs.ParseDocumentStructure(rawDocJSON, gdocs.ParseOptions{
				PreferredTabID: opts.TabID,
				SelectionOnly:  false,
			})
			docData.EditableBlocks = workspace.ReconcileFetchedBlocksWithLocalMapping(docData.EditableBlocks, bundle.EditableBlocks)
			docData.SelectionOnly = bundle.HasPartialScope
			docData.SelectionRanges = bundle.DocSelectionRanges
		}
	} else {
		// Existing Google Document proofreading mode (--doc-id)
		rawDocJSON := existingRawDoc
		if existingDocErr != nil {
			return nil, existingDocErr
		}
		if rawDocJSON == nil {
			var err error
			rawDocJSON, err = docsClient.FetchRawDocumentJSON(ctx, docID)
			if err != nil {
				return nil, err
			}
		}

		selRanges, selectionOnly, rErr := resolveExistingDocRange(rawDocJSON, opts.TabID, opts.RangeSpec)
		if rErr != nil {
			return nil, rErr
		}

		docData = gdocs.ParseDocumentStructure(rawDocJSON, gdocs.ParseOptions{
			PreferredTabID:  opts.TabID,
			SelectionOnly:   selectionOnly,
			SelectionRanges: selRanges,
		})

		// If the existing Google Doc is completely blank and --prompt is provided, auto-generate initial draft first
		if docData.IsEmpty {
			if strings.TrimSpace(opts.Prompt) != "" {
				if isJa {
					spin.Stop("Step 1/4: 対象の Google ドキュメントが白紙のため、初期ドラフト生成モードへ切り替えます")
				} else {
					spin.Stop("Step 1/4: Target Google Document is blank; switching to initial draft generation")
				}
				return executeDraftCommand(ctx, opts, docsClient, geminiClient, spin, resolvedModel, hintOnly, isJa)
			}
			return nil, errors.New("the target Google Document is empty (provide --prompt to generate an initial draft, or pass -i <file> to insert local text)")
		}
	}

	if !opts.NoAuth {
		// Download embedded figure/chart images (VISUAL_1, etc.) in parallel for multimodal review
		visualsWithImages = docsClient.FetchVisualImages(ctx, docData.VisualElements)

		if isJa {
			spin.Stop(fmt.Sprintf("Step 1/4: 準備完了 (DocID=%s, 対象=%d, 参照=%d, RAGルール=%d, ブロック=%d, 図=%d, 表=%d)",
				docID, len(bundle.TargetFiles), len(bundle.ReferenceFiles), len(bundle.RagRules), len(docData.EditableBlocks), len(visualsWithImages), len(docData.Tables)))
		} else {
			spin.Stop(fmt.Sprintf("Step 1/4: Ready (DocID=%s, TargetFiles=%d, ContextFiles=%d, RAGRules=%d, EditableBlocks=%d, Visuals=%d, Tables=%d)",
				docID, len(bundle.TargetFiles), len(bundle.ReferenceFiles), len(bundle.RagRules), len(docData.EditableBlocks), len(visualsWithImages), len(docData.Tables)))
		}
	}

	// Step 2: Use Host-AI pre-generated result, return Phase 1 PreparedContext for Host-AI, or call internal Gemini API
	var proofRes *gemini.ProofreadResult
	if opts.AIResult != nil {
		proofRes = opts.AIResult
		if isJa {
			spin.Stop(fmt.Sprintf("Step 2/4: エージェントAIからテキスト提案 %d件、図表レビュー %d件を受領しました",
				len(proofRes.Suggestions), len(proofRes.ElementSuggestions)))
			spin.Start("Step 3/4: UTF-16 文字座標の照合と一括更新リクエストの構築中...")
		} else {
			spin.Stop(fmt.Sprintf("Step 2/4: Received %d text suggestion(s) and %d figure/table review(s) from Host AI",
				len(proofRes.Suggestions), len(proofRes.ElementSuggestions)))
			spin.Start("Step 3/4: Matching UTF-16 coordinates and building bottom-to-top batchUpdate requests...")
		}
	} else if opts.PrepareOnly || opts.UseAgentAI || geminiClient == nil {
		agentPrompt, schema := gemini.BuildAgentProofreadPrompt(gemini.ProofreadPromptOptions{
			DocData:            docData,
			TargetFiles:        bundle.TargetFiles,
			ReferenceFiles:     bundle.ReferenceFiles,
			RagRules:           bundle.RagRules,
			VisualsWithImages:  visualsWithImages,
			UseDriveConversion: usedDriveConversion,
			Persona:            opts.Persona,
			Lang:               opts.Lang,
			CustomPrompt:       opts.Prompt,
			HintOnly:           hintOnly,
			IncludeResolved:    opts.IncludeResolved,
		})
		var targetBlocks []utf16idx.EditableBlock
		for _, b := range docData.EditableBlocks {
			if b.TargetEditable {
				targetBlocks = append(targetBlocks, b)
			}
		}
		var visSummaries []PreparedVisualSummary
		for idx, v := range docData.VisualElements {
			imgPath := ""
			if idx < len(bundle.EmbeddedImages) {
				imgPath = bundle.EmbeddedImages[idx].OriginalRef
			}
			visSummaries = append(visSummaries, PreparedVisualSummary{
				TargetID:    v.TargetID,
				Type:        v.Type,
				Title:       v.Title,
				Description: v.Description,
				StartIndex:  v.StartIndex,
				EndIndex:    v.EndIndex,
				ImagePath:   imgPath,
			})
		}
		nextInst := fmt.Sprintf("Step 1 complete (Google Doc ready: %s). Now perform Step 2 using your own LLM following coachingPrompt and expectedJsonSchema, then call gdocumentor_proofread AGAIN with docId: %q, skipDocInsert: true, and aiResult: { overallFeedback, learningSummary, existingSuggestionsNote, suggestions, elementSuggestions } (plus the same workDir, inputs/targets, mode, lang, summaryTab, outputPath, or saveLocal parameters) to apply the suggestions and comments to the Google Document.", gdocs.DocumentURL(docID), docID)
		if opts.NoAuth {
			nextInst = "Step 1 complete (Local Mode). Now perform Step 2 using your own LLM following coachingPrompt and expectedJsonSchema, then call gdocumentor_proofread AGAIN with noAuth: true, the same inputs/targets/workDir/mode/lang/outputPath, and aiResult: { overallFeedback, learningSummary, existingSuggestionsNote, suggestions, elementSuggestions } to write the local proofreading/coaching report."
		}
		return &CLIOutput{
			Success:               true,
			Command:               "proofread",
			Model:                 resolvedModel,
			Mode:                  opts.Mode,
			HintOnly:              hintOnly,
			Language:              opts.Lang,
			LocalMode:             opts.NoAuth,
			RequiresAgentAI:       true,
			DocumentID:            docID,
			DocumentURL:           gdocs.DocumentURL(docID),
			DocumentTitle:         docData.Title,
			ActiveTabID:           docData.ActiveTabID,
			CreatedNewDocument:    createdNewDoc,
			SelectionOnly:         docData.SelectionOnly,
			TargetFiles:           extractFilePaths(bundle.TargetFiles),
			ReferenceContextFiles: extractFilePaths(bundle.ReferenceFiles),
			RagRuleFiles:          extractRuleTitles(bundle.RagRules),
			ActivePersona:         activePersona,
			DecisionRecord:        docData.DecisionRecord,
			PreparedContext: &AgentPreparedContext{
				Phase:                "phase1_prepared_for_agent_ai",
				NextStepInstructions: nextInst,
				DocumentID:           docID,
				DocumentURL:          gdocs.DocumentURL(docID),
				ActiveTabID:          docData.ActiveTabID,
				UsedDriveConversion:  usedDriveConversion,
				EditableBlocks:       targetBlocks,
				Tables:               docData.Tables,
				VisualElements:       visSummaries,
				ExistingSuggestions:  docData.ExistingSuggestions,
				ExistingComments:     docData.ExistingComments,
				ResolvedReviews:      docData.ResolvedReviews,
				CoachingPrompt:       agentPrompt,
				ExpectedJSONSchema:   schema,
			},
		}, nil
	} else {
		if isJa {
			spin.Start(fmt.Sprintf("Step 2/4: AI推敲＆コーチング解析を実行中 (%s)...", resolvedModel))
		} else {
			spin.Start(fmt.Sprintf("Step 2/4: Running AI proofreading & coaching analysis (%s)...", resolvedModel))
		}
		var err error
		proofRes, err = geminiClient.GenerateProofreading(ctx, gemini.ProofreadPromptOptions{
			DocData:            docData,
			TargetFiles:        bundle.TargetFiles,
			ReferenceFiles:     bundle.ReferenceFiles,
			RagRules:           bundle.RagRules,
			VisualsWithImages:  visualsWithImages,
			UseDriveConversion: usedDriveConversion,
			Persona:            opts.Persona,
			Lang:               opts.Lang,
			CustomPrompt:       opts.Prompt,
			HintOnly:           hintOnly,
			IncludeResolved:    opts.IncludeResolved,
		})
		if err != nil {
			return nil, err
		}
		if isJa {
			spin.Stop(fmt.Sprintf("Step 2/4: Gemini がテキスト提案 %d件、図表レビュー %d件を生成しました",
				len(proofRes.Suggestions), len(proofRes.ElementSuggestions)))
			spin.Start("Step 3/4: UTF-16 文字座標の照合と一括更新リクエストの構築中...")
		} else {
			spin.Stop(fmt.Sprintf("Step 2/4: Gemini generated %d text suggestion(s) and %d figure/table review(s)",
				len(proofRes.Suggestions), len(proofRes.ElementSuggestions)))
			spin.Start("Step 3/4: Matching UTF-16 coordinates and building bottom-to-top batchUpdate requests...")
		}
	}
	built := gdocs.BuildRequestBody(
		docData,
		proofRes.Suggestions,
		proofRes.ElementSuggestions,
		opts.Lang,
		hintOnly,
	)
	if isJa {
		spin.Stop(fmt.Sprintf("Step 3/4: テキスト提案 %d件、図表コメント %d件を正確な UTF-16 範囲へマッピングしました",
			built.MatchedTextCount, built.MatchedElementCount))
	} else {
		spin.Stop(fmt.Sprintf("Step 3/4: Matched %d text suggestion(s) and %d element comment(s) to exact UTF-16 ranges",
			built.MatchedTextCount, built.MatchedElementCount))
	}

	localOutPath := resolveLocalOutputPath(opts, bundle, docID, false, false, hintOnly)

	if opts.NoAuth {
		if isJa {
			spin.Start(fmt.Sprintf("Step 4/4: 推敲＆コーチングレポートをローカルファイル %q に出力中...", localOutPath))
		} else {
			spin.Start(fmt.Sprintf("Step 4/4: Writing coaching & proofreading report to local file %q...", localOutPath))
		}
		totalApplied := built.MatchedTextCount + built.MatchedElementCount
		out := &CLIOutput{
			Success:                     true,
			Command:                     "proofread",
			Model:                       resolvedModel,
			Mode:                        opts.Mode,
			HintOnly:                    hintOnly,
			Language:                    opts.Lang,
			LocalMode:                   true,
			OutputFilePath:              localOutPath,
			DocumentTitle:               docData.Title,
			SelectionOnly:               docData.SelectionOnly,
			TargetFiles:                 extractFilePaths(bundle.TargetFiles),
			ReferenceContextFiles:       extractFilePaths(bundle.ReferenceFiles),
			RagRuleFiles:                extractRuleTitles(bundle.RagRules),
			ActivePersona:               activePersona,
			OverallFeedback:             proofRes.OverallFeedback,
			LearningSummary:             &proofRes.LearningSummary,
			ExistingSuggestionsNote:     proofRes.ExistingSuggestionsNote,
			AppliedSuggestionsCount:     totalApplied,
			AppliedTextSuggestionsCount: built.MatchedTextCount,
			AppliedElementCommentsCount: built.MatchedElementCount,
			MatchedSuggestions:          built.MatchedItems,
			MatchedElementSuggestions:   built.MatchedElementItems,
		}
		if err := writeLocalProofreadReport(localOutPath, out, bundle, docData, usedDriveConversion, built, isJa); err != nil {
			return nil, err
		}
		if isJa {
			spin.Stop(fmt.Sprintf("Step 4/4: %d件のコーチング項目を %s に保存しました", totalApplied, localOutPath))
		} else {
			spin.Stop(fmt.Sprintf("Step 4/4: Saved %d coaching item(s) to %s", totalApplied, localOutPath))
		}
		return out, nil
	}

	// Step 4: Apply inline suggestions (WriteMode.SUGGEST + addCommentReply) or Hint-Only comments (insertComment)
	if isJa {
		spin.Start("Step 4/4: Google ドキュメントへ提案と教育的解説コメントを適用中...")
	} else {
		spin.Start("Step 4/4: Applying suggestions and educational comments to Google Docs...")
	}
	applyRes, err := docsClient.ApplySuggestions(ctx, docID, built, opts.Lang)
	if err != nil {
		return nil, err
	}
	if isJa {
		spin.Stop(fmt.Sprintf("Step 4/4: Google ドキュメントへ %d件の提案・コメントを適用しました", applyRes.AppliedSuggestionsCount))
	} else {
		spin.Stop(fmt.Sprintf("Step 4/4: Applied %d suggestion(s)/comment(s) to Google Document", applyRes.AppliedSuggestionsCount))
	}

	out := &CLIOutput{
		Success:                     true,
		Command:                     "proofread",
		Model:                       resolvedModel,
		Mode:                        opts.Mode,
		HintOnly:                    hintOnly,
		Language:                    opts.Lang,
		OutputFilePath:              localOutPath,
		DocumentID:                  docID,
		DocumentURL:                 gdocs.DocumentURL(docID),
		DocumentTitle:               docData.Title,
		ActiveTabID:                 docData.ActiveTabID,
		CreatedNewDocument:          createdNewDoc,
		SelectionOnly:               docData.SelectionOnly,
		TargetFiles:                 extractFilePaths(bundle.TargetFiles),
		ReferenceContextFiles:       extractFilePaths(bundle.ReferenceFiles),
		RagRuleFiles:                extractRuleTitles(bundle.RagRules),
		ActivePersona:               activePersona,
		OverallFeedback:             proofRes.OverallFeedback,
		LearningSummary:             &proofRes.LearningSummary,
		ExistingSuggestionsNote:     proofRes.ExistingSuggestionsNote,
		AppliedSuggestionsCount:     applyRes.AppliedSuggestionsCount,
		AppliedTextSuggestionsCount: applyRes.AppliedTextSuggestionsCount,
		AppliedElementCommentsCount: applyRes.AppliedElementCommentsCount,
		MatchedSuggestions:          built.MatchedItems,
		MatchedElementSuggestions:   built.MatchedElementItems,
		DecisionRecord:              applyRes.DecisionRecord,
	}
	if opts.SummaryTab {
		if isJa {
			spin.Start("Step 4/4: Google ドキュメント内のまとめ専用タブを作成・更新中...")
		} else {
			spin.Start("Step 4/4: Creating/updating Google Docs Coaching Summary Tab...")
		}
		summaryContent, summaryHeadings := buildGoogleDocsSummaryTabContent(out, isJa)
		tabRes, tabErr := docsClient.UpsertDocumentSummaryTab(ctx, docID, opts.SummaryTabName, summaryContent, summaryHeadings)
		if tabErr != nil {
			return nil, tabErr
		}
		out.SummaryTabID = tabRes.TabID
		out.SummaryTabTitle = tabRes.TabTitle
		out.SummaryTabURL = tabRes.TabURL
		out.SummaryTabCreatedNew = tabRes.CreatedNewTab
		if isJa {
			spin.Stop(fmt.Sprintf("Step 4/4: Google ドキュメント・まとめタブ %q (%s) を更新しました", tabRes.TabTitle, tabRes.TabID))
		} else {
			spin.Stop(fmt.Sprintf("Step 4/4: Updated Google Docs Summary Tab %q (%s)", tabRes.TabTitle, tabRes.TabID))
		}
	}
	if localOutPath != "" {
		if err := writeLocalProofreadReport(localOutPath, out, bundle, docData, usedDriveConversion, built, isJa); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func emitError(stdout, stderr io.Writer, jsonMode bool, err error) error {
	if jsonMode && stdout != nil {
		errPayload := map[string]any{
			"success": false,
			"error":   err.Error(),
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(errPayload)
	}
	if stderr != nil {
		fmt.Fprintf(stderr, "%sError:%s %v\n", ansiRed, ansiReset, err)
	}
	return err
}

func splitCommaOrKeep(v string) []string {
	var out []string
	for part := range strings.SplitSeq(v, ",") {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func parseBoolStr(v string, defaultVal bool) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return defaultVal
	}
}

func applyJSONPresenceFlags(rawJSON []byte, opts *Options) {
	if opts.EditText != "" {
		opts.HasEditText = true
	}
	if opts.ReplaceText != "" {
		opts.HasReplace = true
	}
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(rawJSON, &rawMap); err == nil {
		if _, ok := rawMap["text"]; ok {
			opts.HasEditText = true
		}
		if _, ok := rawMap["replace"]; ok {
			opts.HasReplace = true
		}
		if _, ok := rawMap["markdown"]; ok {
			b := opts.Markdown
			opts.ExplicitMD = &b
		}
		if rawVal, ok := rawMap["useAgentAI"]; ok {
			var b bool
			if json.Unmarshal(rawVal, &b) == nil {
				opts.UseAgentAI = b
			}
		}
	}
}

func extractFilePaths(entries []workspace.FileEntry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.DisplayPath)
	}
	return out
}

func extractRuleTitles(rules []workspace.RagRuleFile) []string {
	var out []string
	for _, r := range rules {
		out = append(out, fmt.Sprintf("%s (%s)", r.Title, r.SourceType))
	}
	return out
}

func isPipedStdin(r io.Reader) bool {
	if r == nil {
		return false
	}
	f, ok := r.(*os.File)
	if !ok {
		return true
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) == 0
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

