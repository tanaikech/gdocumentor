package cli

import (
	"fmt"
	"io"
	"strings"
)

func normalizeHelpTopic(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "proofread":
		return "proofread"
	case "summary":
		return "summary"
	case "draft":
		return "draft"
	case "edit":
		return "edit"
	case "manage":
		return "manage"
	case "inspect":
		return "inspect"
	case "mcp", "--mcp":
		return "mcp"
	case "setup", "install-agent", "install-mcp", "install-skill":
		return "setup"
	default:
		return ""
	}
}

// PrintSubcommandHelp writes detailed subcommand-specific usage, flags, and sample commands to w.
func PrintSubcommandHelp(w io.Writer, topic string) {
	switch normalizeHelpTopic(topic) {
	case "setup":
		printSetupHelp(w)
	case "proofread":
		printProofreadHelp(w)
	case "summary":
		printSummaryHelp(w)
	case "draft":
		printDraftHelp(w)
	case "edit":
		printEditHelp(w)
	case "manage":
		printManageHelp(w)
	case "inspect":
		printInspectHelp(w)
	case "mcp":
		printMCPHelp(w)
	default:
		PrintHelp(w)
	}
}

// PrintHelp writes the comprehensive English CLI usage guide to w.
func PrintHelp(w io.Writer) {
	helpText := "gdocumentor — DocuMentor AI (Go CLI, Dual-Engine MCP Server & Multi-Agent Skill v" + Version + `)` + `
Transform AI from a "Ghostwriter" into a "Writing & Code Review Coach" on Google Docs and Local Files.

USAGE:
  gdocumentor [subcommand] [flags] [files/directories...]
  gdocumentor <subcommand> --help   (or -h: show detailed help & examples for a specific subcommand)

SUBCOMMANDS:
  proofread   Proofread local files/code or an existing Google Doc and apply inline suggestions/comments (default when -i/-t/-d is passed)
  summary     Analyze existing inline suggestions & comments on a Google Doc and generate a Learning Summary
  draft       Generate a structured initial draft into a new or blank Google Doc (or local file) from a topic/prompt
  edit        Create/delete a Google Doc, insert text/Markdown/images, replace text/ranges, or delete ranges (0 Gemini tokens)
  manage      Accept/reject all inline suggestions, delete comments, reset Decision Record, or create/delete a Google Doc
  inspect     List paragraph numbers (#1, #2...), exact UTF-16 (@start:end) coordinates, and review threads on a Google Doc
  mcp         Start the Model Context Protocol (MCP) JSON-RPC 2.0 server over stdio for any AI client (0 GEMINI_API_KEY needed)
  setup       Install, inspect (--status), or uninstall (--uninstall) gdocumentor MCP server & skills across AI clients
  version     Print version information
  help        Show this help message (or 'gdocumentor help <subcommand>')

TIP — SUBCOMMAND HELP:
  Running 'gdocumentor' or 'gdocumentor <subcommand>' with no arguments (or with --help / -h) displays detailed usage:
    gdocumentor setup --help       Detailed multi-agent & cross-platform (WSL2 <-> Windows) installer guide
    gdocumentor proofread --help   Detailed proofreading, Socratic Hint-Only, RAG, and partial-range guide
    gdocumentor summary --help     Detailed review thread analysis & Coaching Summary Tab guide
    gdocumentor draft --help       Detailed initial draft generation guide
    gdocumentor edit --help        Detailed 0-token Google Docs text/Markdown/table/image editing guide
    gdocumentor manage --help      Detailed batch suggestion accept/reject & comment cleanup guide
    gdocumentor inspect --help     Detailed paragraph & UTF-16 coordinate inspection guide
    gdocumentor mcp --help         Detailed Dual-Engine MCP Server & Host-AI workflow guide

INPUT & SCOPE FLAGS:
  -w, --work-dir <dir>        Working directory for resolving relative input, context, rule, and output paths
  -i, --input <path|url>      Target file(s) (.md, .txt, .pdf, .docx, scripts/code), dir, URL, or '-' for stdin
  -t, --target <spec>         Target file/URL with line or char range, e.g. "main.go:15-45", "url#L15-L45", "doc.md@100:250"
      --repo <github-url>     Shallow-clone a GitHub repository URL and resolve -i/-t/-c/--auto-deps inside it
      --range <spec>          Paragraph/line range ("2-5"), UTF-16 offset range ("@100:600"), or text phrase on Docs
  -c, --context <path|url>    Explicit read-only reference context file(s), directory, or URL for consistency checks
      --auto-deps             Automatically resolve local imports/includes & adjacent Markdown chapters (default: true)
      --no-auto-deps          Disable automatic dependency and adjacent file resolution

RAG / ORGANIZATION GUIDELINES & AI PERSONA FLAGS:
  -r, --rules <path|dir|url>  Rulebook file(s), directory (loads all rule files inside), or URL (.md, .txt, .docx, .pdf)
      --rules-dir <dir>       Directory of local RAG rulebook files (loads all rule files inside)
      --drive-rag <id|url>    Google Drive Folder or File ID/URL containing organization rulebooks
      --persona <text>        Custom AI coach persona/positioning (can also be defined inside any RAG rulebook file)

GOOGLE DOCS & WORKSPACE FLAGS:
  -d, --doc-id <id|url>       Existing Google Document ID or full URL (if omitted with local files, creates a new Doc)
      --title <string>        Custom title when creating a new Google Document
      --parent-folder <id>    Google Drive Folder ID or URL where the new Google Document should be created
      --tab-id <id>           Target tab ID within a multi-tab Google Document (e.g. "t.0")
      --summary-tab           Create or update a dedicated Coaching Summary Tab ("🎓 DocuMentor AI Summary") in the Google Doc
      --summary-tab-name <t>  Custom tab/tag name for the Google Docs Coaching Summary Tab (automatically enables --summary-tab)

COACHING & AI MODEL FLAGS:
  -m, --mode <suggest|hint>   "suggest" (WriteMode.SUGGEST + educational reply) or "hint" (Socratic coaching comments)
      --include-resolved      Include resolved/accepted/rejected review threads as coaching context (default: true)
      --no-include-resolved   Exclude resolved review threads from AI context
      --model <name>          Gemini model: "gemini-3.8-flash" (default), "gemini-4-hydrogen", "gemini-4-carbon", "gemini-4-argon", or any model ID
  -l, --lang <code>           Coaching feedback & comment language: "en" (default), "ja", "de", "fr", "es", "zh", "ko", "auto", etc.
  -p, --prompt <text>         Custom focus instructions for proofreading or topic outline for draft generation

DIRECT GOOGLE DOCS EDITING FLAGS (edit SUBCOMMAND — 0 GEMINI TOKENS):
      --create-doc, --create  Create a new Google Document (optionally with --title, --parent-folder, and initial text/file)
      --delete-doc            Permanently delete the Google Document from Google Drive
      --text, --insert <str>  Text to insert or write (can also be passed via -i <file>, pipe '|', '<<<', '<<EOF', or '< file')
      --prepend, --start      Insert text at the very beginning of the document (index 1)
      --append, --end         Insert text at the end of the document (default when inserting into an existing Doc)
      --at <spec>             Insert at exact UTF-16 offset ("@150"), 1-based paragraph ("3"), "start", or "end"
      --index <num>           Insert at exact 1-based UTF-16 character index (e.g. "150" or "@150")
      --before <phrase>       Insert text immediately before the matching text/heading in the document
      --after <phrase>        Insert text immediately after the matching text/heading in the document
      --before-paragraph <N>  Insert text before 1-based paragraph number N
      --after-paragraph <N>   Insert text after 1-based paragraph number N
      --style <style>         Apply named paragraph style to inserted text ("HEADING_1".."HEADING_6", "TITLE", "NORMAL_TEXT")
      --markdown, --md        Convert Markdown syntax (headings, bold/italic/code/links, lists, tables, images) to Google Docs elements
      --raw-text              Insert .md file content as literal plain text without Markdown conversion
      --insert-image <src>    Insert a local image file (.png/.jpg/.gif/.webp) or image URL at the target location
      --replace-image <id>    Replace or resize an existing image ("VISUAL_1", "1", objectId, "@index", or "all")
      --resize-image <id>     Resize an existing image ("VISUAL_1", "1", objectId, "@index", or "all") in place (use with --image-width / --image-height)
      --with-image <src>      Replacement local image file path or image URL for --replace-image (optional when resizing in place)
      --delete-image <id>     Delete an existing image ("VISUAL_1", "1", objectId, "@index", or "all") from the document
      --image-width <pt>      Optional width in points (PT) for inserted, replaced, or resized images
      --image-height <pt>     Optional height in points (PT) for inserted, replaced, or resized images
      --find <phrase>         Search text/phrase to replace (use with --replace, --text, -i <file>, or stdin)
      --replace <str>         Replacement text for --find or --replace-range
      --replace-range <spec>  Replace a specific range ("@100:250", "2-4", "all", or phrase) with new text
      --replace-body          Replace the entire body content of the target Google Document tab
      --delete-range <spec>   Delete a specific range ("@100:250", "3", "2-5", or "all") from the document
      --delete-text <phrase>  Delete occurrences of the specified text/phrase from the document
      --clear                 Clear all body text in the target Google Document tab (leaves document blank)
      --ignore-case           Case-insensitive matching for --find, --delete-text, --before, and --after
      --occurrence <N|all>    Target only the N-th occurrence (1-based) or "all" (0)
      --suggest               Apply text insertion/replacement/deletion as a Google Docs suggestion (WriteMode.SUGGEST)

MANAGE SUBCOMMAND FLAGS:
      --create-doc            Create a new blank Google Document on Google Drive
      --accept-all            Accept all pending inline suggestions in the document
      --reject-all            Reject (clear) all pending inline suggestions in the document
      --delete-comments       Delete all active comment threads in the document
      --reset-record          Reset the Coach Impact & Decision Record statistics for the document
      --delete-doc            Permanently delete the Google Document from Google Drive (and reset its Decision Record)

AUTHENTICATION, LOCAL FILE OUTPUT, MCP SERVER & AGENT SKILL FLAGS:
      --no-auth, --local      Run WITHOUT a Google OAuth2 Access Token (uses ONLY Gemini API and outputs to a local file)
  -o, --output <file-path>    Save local Markdown report/hints/draft/summary (works BOTH with --no-auth AND with Access Token)
      --save-local            Also save local Markdown report (<stem>_proofread.md / _hint.md) when using an Access Token
      --mcp                   Start the Model Context Protocol (MCP) JSON-RPC 2.0 server over stdio (uses Client Agent AI by default)
      --prepare-only          Execute Step 1 (resolve files/RAG/Google Doc) and return PreparedContext for external Agent AI (0 Gemini tokens)
      --use-agent-ai          Bypass internal Gemini API and use Client Agent AI two-phase or pre-generated execution
      --use-internal-gemini   Force internal Gemini API execution even when running in MCP mode
      --skip-doc-insert       Skip re-inserting local file text when --doc-id already contains text from Phase 1
      --base-revision-id <id> Optional Google Docs revisionId from Phase 1 (--prepare-only) for concurrent edit re-anchoring
      --ai-result <json|@f>   Apply pre-generated ProofreadResult JSON from an external AI agent without calling Gemini API
      --ai-summary-result <j> Apply pre-generated SummaryResult JSON from an external AI agent without calling Gemini API
      --generated-draft <txt> Insert pre-generated initial draft text (or @file) without calling Gemini API
      --api-key <key>         Gemini API key (overrides GEMINI_API_KEY environment variable)
      --gemini-base-url <url> Optional custom Gemini API endpoint / reverse proxy URL (overrides GEMINI_BASE_URL)
      --access-token <token>  Google OAuth2 access token (overrides ADC / gcloud)
      --quota-project <id>    Google Cloud Quota Project ID for X-Goog-User-Project header
      --input-json <json|@f>  Pass all options as a structured JSON string or @file.json
      --json                  Output ONLY pure, machine-readable JSON to stdout (disables TUI & spinner)

MULTI-AGENT PLUGIN & MCP SETUP FLAGS (setup SUBCOMMAND — run 'gdocumentor setup --help' for full details):
  -a, --all, --install-all    Automatically install/configure gdocumentor across ALL 7 supported AI clients
      --agent <name[,...]>    Target specific AI client(s): "antigravity", "claude-desktop", "claude-code", "cursor", "vscode", "windsurf", "gemini-cli", or "all"
      --scope <user|project>  Installation scope: "user" (global config, default), "project" (current workspace), or "both"
      --wsl-host              Configure Windows 11 host GUI apps (Claude Desktop, Cursor, VS Code, Windsurf) from inside WSL2 via wsl.exe --exec
      --wsl-distro <name>     Optional WSL2 distribution name for --wsl-host ("wsl.exe -d <name> --exec ...")
      --win-user <username>   Optional Windows host username override for /mnt/c/Users/<username>
      --binary-path <path>    Explicit path to the gdocumentor binary to register in MCP configurations
      --status                Inspect current installation status of gdocumentor across supported AI clients
      --uninstall             Cleanly remove gdocumentor MCP server and skill entries from the specified AI client(s)

GCLOUD CLI QUICK INSTALL & AUTH (FOR GOOGLE DOCS & DRIVE MODE):
  If gcloud CLI is not installed, install it and log in with Application Default Credentials (ADC):
    • Linux / macOS:  curl https://sdk.cloud.google.com | bash && exec -l $SHELL
    • Ubuntu/Debian:  sudo apt-get update && sudo apt-get install -y google-cloud-cli
    • macOS (Brew):   brew install --cask google-cloud-sdk
    • Windows:        winget install Google.CloudSDK   (Docs: https://cloud.google.com/sdk/docs/install)
    • Login (ADC):    gcloud auth application-default login --scopes="https://www.googleapis.com/auth/documents,https://www.googleapis.com/auth/drive,https://www.googleapis.com/auth/cloud-platform"

EXAMPLES:
  # 0. Multi-Agent MCP & Skill Setup (Native OS & Cross-Platform WSL2 <-> Windows 11 Host)
  gdocumentor setup --help                                           # Show detailed setup help & cross-platform guide
  gdocumentor setup --all                                            # Install for ALL 7 AI clients on current OS
  gdocumentor setup --agent antigravity,claude-code                  # Install for specific AI clients on current OS
  gdocumentor setup --all --wsl-host                                 # From WSL2: configure ALL Windows 11 Host GUI clients via wsl.exe --exec
  gdocumentor setup --agent claude-desktop --wsl-host                # From WSL2: configure Windows 11 Claude Desktop via wsl.exe --exec
  gdocumentor setup --status                                         # Check installation status on current OS
  gdocumentor setup --status --wsl-host                              # From WSL2: check installation status on Windows 11 Host

  # 1. Access-Token-Free Local Mode (--no-auth: requires ONLY GEMINI_API_KEY; outputs suggestions or hints to a local file)
  gdocumentor --no-auth -i sample/sample_article.md -o review_report.md -l ja
  gdocumentor --no-auth -i "https://go.dev/doc/effective_go" --range "1-60" --hint -o effective_go_hints.md -l ja

  # 2. Multimodal Markdown Proofreading into Google Docs (and optionally ALSO save a local Markdown report via -o or --save-local)
  gdocumentor -i sample/sample_article.md -l ja
  gdocumentor -i sample/sample_article.md --summary-tab -o review_report.md -l ja

  # 3. Convert & proofread a local PDF or Word (.docx) file directly in Google Docs
  gdocumentor -i report.pdf -l ja
  gdocumentor -i manuscript.docx --range "2-6" -l ja

  # 4. Customize the AI Coach Persona via RAG rulebook (-r) or --persona (e.g. Japanese University Professor)
  gdocumentor -i sample/sample_article.md --persona "あなたは大学で日本語を教えている日本人です。" -l ja

  # 5. Proofread ONLY lines 15-80 of a Go script (inserted into a new Google Doc) with auto-deps & style rulebook (-r)
  gdocumentor -t "internal/cli/app.go:15-80" -r sample/rules/coding_standards.md --auto-deps -p "Check error handling and naming"

  # 6. Load ALL rule files (.md, .txt, .docx, .pdf, including AI persona definitions) inside a directory as RAG (-r <dir>)
  gdocumentor -i sample/sample_article.md -r ./sample/rules/ -l ja

  # 7. Character-offset partial proofreading (@start:end) & Socratic Hint-Only Mode (-m hint / --hint)
  gdocumentor -t "manuscript/chapter1.md@100:450" -m hint -l ja

  # 8. Proofread directly from a GitHub Blob URL (#L15-L60) or a GitHub repository (--repo) with sibling files
  gdocumentor -t "https://github.com/tanaikech/DocuMentorAI/blob/main/src/code.js#L15-L60" --auto-deps -l ja
  gdocumentor --repo "https://github.com/tanaikech/DocuMentorAI" -t "src/code.js:1-80" -c README.md --auto-deps -l ja

  # 9. In-place proofreading (including embedded Google Docs images) or Hint-Only coaching on an existing Google Doc (-d)
  gdocumentor -d "https://docs.google.com/document/d/1abc123.../edit" -l ja
  gdocumentor -d "https://docs.google.com/document/d/1abc123.../edit" -m hint --range "2-5" -l ja

  # 10. Inspect paragraph numbers (#1, #2...), UTF-16 (@start:end) coordinates, & Coach Decision Record (0 Gemini tokens)
  gdocumentor inspect -d "https://docs.google.com/document/d/1abc123.../edit"

  # 11. Read an existing Google Doc and output ONLY the Writing Coach & Learning Summary (no document edits)
  gdocumentor summary -d "https://docs.google.com/document/d/1abc123.../edit" --include-resolved -l ja

  # 12. Generate a structured initial draft into a new Google Doc from a topic prompt & template rulebook
  gdocumentor draft --title "Design RFC" -r sample/rules/writing_guidelines.md -p "Propose a hybrid OAuth2 token cache in Go."

  # 13. Direct Google Docs Editing (Create, Insert via File/Pipe/Here-String/Heredoc/Redirect, Replace, Delete — 0 Gemini tokens)
  gdocumentor edit --create-doc --title "New Blank Doc"
  gdocumentor edit -d "1abc123..." --prepend <<< "Inserted at the very beginning via here-string"
  gdocumentor edit -d "1abc123..." --find "draft version" --replace "v1.0.0 release"
  gdocumentor edit -d "1abc123..." --resize-image VISUAL_1 --image-width 320

  # 14. Batch accept/reject all pending suggestions, delete comments, reset Decision Record, or delete the Google Doc
  gdocumentor manage -d "1abc123..." --accept-all
  gdocumentor manage -d "1abc123..." --reject-all --delete-comments
  gdocumentor manage -d "1abc123..." --delete-doc

  # 15. Start or test the Model Context Protocol (MCP) JSON-RPC 2.0 server over stdio
  gdocumentor mcp
`
	fmt.Fprint(w, helpText)
}

func printSetupHelp(w io.Writer) {
	text := `gdocumentor setup — Multi-Agent MCP Server & Skill Installer / Uninstaller
Configure gdocumentor across Google Antigravity, Claude Desktop, Claude Code, Cursor, VS Code (GitHub Copilot), Windsurf, and Gemini CLI.
When used as an MCP server, gdocumentor runs in Host-AI Mode (0 internal Gemini API tokens; GEMINI_API_KEY is NOT required).

USAGE:
  gdocumentor setup --all [flags]
  gdocumentor setup --agent <client[,client...]> [flags]
  gdocumentor setup --status [flags]
  gdocumentor setup --uninstall (--all | --agent <client[,client...]>) [flags]

SUPPORTED AI CLIENTS (--agent <name>):
  antigravity      Google Antigravity IDE & CLI (aliases: agy, antigravity-cli, antigravity-ide)
  claude-desktop   Anthropic Claude Desktop App (aliases: claudedesktop, claude-app)
  claude-code      Anthropic Claude Code CLI (aliases: claude, claudecode, claude-cli)
  cursor           Cursor IDE & Agent CLI (aliases: cursor-ide, cursor-cli)
  vscode           Visual Studio Code & GitHub Copilot (aliases: code, copilot, github-copilot)
  windsurf         Windsurf IDE / Codeium (aliases: codeium, windsurf-ide)
  gemini-cli       Google Gemini CLI (aliases: gemini, geminicli)
  all              All 7 supported AI clients above (same as passing -a / --all)

FLAGS:
  -a, --all, --install-all      Automatically install/configure ALL 7 supported AI clients at once
      --agent, --client <list>  Target specific AI client(s), comma-separated (e.g. "claude-desktop" or "antigravity,cursor,vscode")
      --scope <user|project|both>
                                Configuration scope (default: "user"):
                                  • user:    Global user profile config (~/.gemini, ~/.claude.json, %APPDATA%/Claude, etc.)
                                  • project: Current workspace directory (mcp_config.json, .mcp.json, .cursor/mcp.json, .vscode/mcp.json)
                                  • both:    Install into both global user profile and current project workspace
      --wsl-host, --wsl         [WSL2 -> Windows 11 Bridge] Run from inside WSL2 to configure Windows 11 host GUI apps
                                (Claude Desktop, Cursor, VS Code, Windsurf) under /mnt/c/Users/<WinUser>/AppData/Roaming/...
                                using "command": "wsl.exe", "args": ["--exec", "<wsl-gdocumentor-path>", "mcp"]
      --wsl-distro <name>       Optional WSL2 distribution name for --wsl-host (generates "wsl.exe -d <name> --exec ...")
      --win-user <username>     Optional Windows 11 host username override if /mnt/c/Users/<username> differs from WSL $USER
      --binary-path <path>      Explicit path to the gdocumentor binary to register in MCP config files
  -w, --work-dir <dir>          Target project workspace directory when using --scope project or --scope both
      --status                  Inspect and display current MCP & Skill installation status (does not modify files)
      --uninstall               Cleanly remove gdocumentor_server from MCP config files (preserving other servers) and remove skills
      --json                    Output structured SetupReport JSON to stdout
  -h, --help                    Show this detailed help message for the 'setup' subcommand

================================================================================
🌐 CROSS-PLATFORM INSTALLATION GUIDE (LINUX / MACOS / WINDOWS 11 / WSL2)
================================================================================

1. NATIVE SINGLE-OS SETUP (Linux, macOS, or Native Windows PowerShell):
   Use this when your AI clients and gdocumentor binary run on the SAME operating system.
     # Install for ALL 7 supported AI clients at once (User-global scope)
     gdocumentor setup --all

     # Install ONLY for specific AI client(s)
     gdocumentor setup --agent antigravity
     gdocumentor setup --agent claude-desktop
     gdocumentor setup --agent claude-code,cursor,vscode

     # Install project-scoped MCP & skill files into the current repository
     gdocumentor setup --all --scope project
     gdocumentor setup --agent cursor,vscode --scope both

2. CROSS-PLATFORM WSL2 (LINUX) <-> WINDOWS 11 HOST SETUP (--wsl-host):
   Use this when you develop inside WSL2 (where the Linux 'gdocumentor' binary and 'gcloud' OAuth2 ADC
   credentials live), while running GUI AI apps (Claude Desktop, Cursor, VS Code, Windsurf) on Windows 11:

     # [Step A] Inside WSL2: Configure CLI/IDE agents running inside WSL2 Linux (Antigravity, Claude Code, Gemini CLI)
     gdocumentor setup --agent antigravity,claude-code,gemini-cli

     # [Step B] Inside WSL2: Configure ALL Windows 11 Host GUI clients via the WSL2 Bridge (--wsl-host)
     gdocumentor setup --all --wsl-host

     # [Step B - Single Client] Or configure ONLY Windows 11 Claude Desktop from inside WSL2
     gdocumentor setup --agent claude-desktop --wsl-host

     # [Step B - Custom Distro / Windows Username] Pin a specific WSL distro and Windows username
     gdocumentor setup --agent claude-desktop,cursor,vscode --wsl-host --wsl-distro Ubuntu-24.04 --win-user Alice

   How --wsl-host works:
     • Safely merges "gdocumentor_server" into /mnt/c/Users/<WinUser>/AppData/Roaming/Claude/claude_desktop_config.json
       (and Cursor / VS Code / Windsurf configs on Windows) without overwriting your existing MCP servers.
     • Registers: "command": "wsl.exe", "args": ["--exec", "/home/<user>/.local/bin/gdocumentor", "mcp"]
     • Automatically translates Windows paths (C:\Users\...) and WSL paths (/mnt/c/Users/...) in both directions!

3. NATIVE WINDOWS 11 SETUP (PowerShell / Command Prompt without WSL2):
   If you use the native Windows binary (gdocumentor_windows_amd64.exe renamed to gdocumentor.exe):
     gdocumentor.exe setup --all
     gdocumentor.exe setup --agent claude-desktop,vscode,cursor

4. CHECKING STATUS & UNINSTALLING:
     # Check installation status on current OS
     gdocumentor setup --status

     # Check installation status on Windows 11 Host from inside WSL2
     gdocumentor setup --status --wsl-host

     # Uninstall from a specific client or all clients
     gdocumentor setup --uninstall --agent claude-desktop
     gdocumentor setup --uninstall --all
     gdocumentor setup --uninstall --all --wsl-host
`
	fmt.Fprint(w, text)
}

func printProofreadHelp(w io.Writer) {
	text := `gdocumentor proofread — AI Writing & Code Review Coach (Google Docs & Local Mode)
Proofread local manuscripts (.md with automatic Base64 image embedding, .txt, .pdf, .docx), source code (.go, .py, .js, .ts, .sh, etc.),
specific line/character ranges, GitHub repositories/URLs, Web URLs, or existing Google Documents.

USAGE:
  gdocumentor [proofread] -i <file|dir|url> [flags]
  gdocumentor [proofread] -t <file:startLine-endLine | file@startChar:endChar> [flags]
  gdocumentor [proofread] -d <google-doc-id-or-url> [flags]
  gdocumentor [proofread] --no-auth -i <file|url> -o <report.md> [flags]

KEY FLAGS:
  -i, --input <path|url>        Target file(s), directory, Web URL, or '-' for stdin
  -t, --target <spec>           Target with line range ("main.go:15-80", "url#L15-L80") or char range ("doc.md@100:450")
      --range <spec>            Standalone paragraph/line range ("2-5"), UTF-16 range ("@100:600"), or section heading phrase
      --repo <github-url>       Shallow-clone a GitHub repository and resolve -i/-t/-c/--auto-deps inside it
  -c, --context <path|url>      Read-only reference context file(s), directory, or URL for cross-file consistency checks
      --auto-deps               Automatically attach local imports/includes & sibling Markdown files (default: true)
      --no-auto-deps            Disable automatic dependency discovery
  -r, --rules <path|dir|url>    RAG rulebook file(s), directory, or URL (.md, .txt, .json, .yaml, .docx, .pdf)
      --rules-dir <dir>         Directory of local RAG rulebook files
      --drive-rag <id|url>      Google Drive Folder or File ID/URL containing organization rulebooks
      --persona <text>          Custom AI coach persona/role (e.g. "あなたは大学で日本語を教えている日本人です。")
  -d, --doc-id <id|url>         Existing Google Document ID or URL for in-place review (creates a new Pageless Doc if omitted)
      --title <string>          Custom title when creating a new Google Document
      --parent-folder <id|url>  Google Drive Folder ID/URL where the new Google Document should be created
      --tab-id <id>             Target tab ID within a multi-tab Google Document
      --summary-tab             Create/update a dedicated Coaching Summary Tab ("🎓 DocuMentor AI Summary") inside the Google Doc
      --summary-tab-name <name> Custom tab title for the Google Docs Coaching Summary Tab (automatically enables --summary-tab)
  -m, --mode <suggest|hint>     "suggest" (default: native WriteMode.SUGGEST edits + rationale reply) or "hint" (Socratic comments)
      --hint                    Shorthand for -m hint (Socratic Hint-Only coaching mode)
  -l, --lang <code>             Feedback & comment language: "en" (default), "ja", "de", "fr", "es", "zh", "ko", "auto", etc.
  -p, --prompt <text>           Custom focus instructions for proofreading
      --model <name>            Gemini model in standalone CLI mode (default: "gemini-3.8-flash"; also "gemini-4-hydrogen", "gemini-4-carbon", "gemini-4-argon")
      --no-auth, --local        Run WITHOUT Google OAuth2 (outputs inline diff/hint report to a local Markdown file)
  -o, --output <file-path>      Write local Markdown report (works in BOTH --no-auth mode AND Google Docs mode)
      --save-local              Also save local Markdown report (<stem>_proofread.md / <stem>_hint.md) alongside Google Docs
  -w, --work-dir <dir>          Base working directory for resolving relative paths
      --base-revision-id <id>   Optional Phase 1 Google Docs revisionId for 2-step optimistic re-anchoring
      --json                    Output pure JSON to stdout

EXAMPLES:
  # 1. Local Mode (--no-auth: no Google account needed; writes inline [-old-]{+new+} diff or Socratic hints to local file)
  gdocumentor proofread --no-auth -i sample/sample_article.md -o review_report.md -l ja
  gdocumentor proofread --no-auth -i sample/sample_article.md --hint -o hints_report.md -l ja

  # 2. Google Docs Mode: Convert Markdown (with local images) into a Pageless Google Doc + Summary Tab
  gdocumentor proofread -i sample/sample_article.md --summary-tab -l ja
  gdocumentor proofread -i sample/sample_article.md --summary-tab="AI推敲まとめ" -o local_copy.md -l ja

  # 3. Partial Line-Range Code Review with Automatic Dependency Resolution (--auto-deps) & Style Rulebook (-r)
  gdocumentor proofread -t "internal/cli/app.go:15-80" -r sample/rules/ --auto-deps -l ja

  # 4. Socratic Hint-Only Coaching on an Existing Google Document (-d) for Paragraphs 2 to 5
  gdocumentor proofread -d "https://docs.google.com/document/d/1abc123.../edit" -m hint --range "2-5" -l ja

  # 5. Remote GitHub Repository or Blob URL Review
  gdocumentor proofread -t "https://github.com/tanaikech/DocuMentorAI/blob/main/src/code.js#L15-L60" --auto-deps -l ja
  gdocumentor proofread --repo "https://github.com/tanaikech/DocuMentorAI" -t "src/code.js:1-80" -c README.md -l ja
`
	fmt.Fprint(w, text)
}

func printSummaryHelp(w io.Writer) {
	text := `gdocumentor summary — Review Thread Analysis & Learning Summary Generator
Read an existing Google Document (including pending inline suggestions with 🤖 AI vs 👤 Human provenance, open comments,
resolved review history, tables, and embedded images) and generate ONLY the Writing Coach & Learning Summary and
Coach Impact & Decision Record without modifying the document body.

USAGE:
  gdocumentor summary -d <google-doc-id-or-url> [flags]

FLAGS:
  -d, --doc-id <id|url>         Target Google Document ID or full URL (required)
      --tab-id <id>             Target tab ID within a multi-tab Google Document (e.g. "t.0")
      --range <spec>            Optional scope filter: paragraph range ("2-5"), UTF-16 range ("@100:600"), or heading phrase
      --include-resolved        Include resolved/accepted/rejected review threads in the analysis (default: true)
      --no-include-resolved     Analyze only currently open/pending suggestions and comments
      --summary-tab             Write/update the generated summary into a dedicated Google Docs Tab ("🎓 DocuMentor AI Summary")
      --summary-tab-name <name> Custom title for the Google Docs Coaching Summary Tab (automatically enables --summary-tab)
  -r, --rules <path|dir|url>    Optional RAG rulebook file(s), directory, or URL to evaluate reviews against
      --drive-rag <id|url>      Optional Google Drive Folder/File ID or URL containing organization rulebooks
      --persona <text>          Optional custom AI coach persona
  -l, --lang <code>             Output language: "en" (default), "ja", "de", "fr", "es", "zh", "ko", "auto", etc.
  -p, --prompt <text>           Custom focus instructions for the summary analysis
  -o, --output <file-path>      Save the generated Learning Summary report to a local Markdown file
      --save-local              Save the generated Learning Summary to <docId>_summary.md
      --model <name>            Gemini model in standalone CLI mode (default: "gemini-3.8-flash"; also "gemini-4-hydrogen", "gemini-4-carbon", "gemini-4-argon")
      --json                    Output pure JSON to stdout

EXAMPLES:
  # 1. Summarize all pending and resolved review threads on an existing Google Doc in Japanese
  gdocumentor summary -d "https://docs.google.com/document/d/1abc123.../edit" --include-resolved -l ja

  # 2. Summarize review threads AND write the result into a dedicated Google Docs Summary Tab + local file
  gdocumentor summary -d "1abc123..." --summary-tab -o summary_report.md -l ja

  # 3. Summarize only a specific section or UTF-16 character range
  gdocumentor summary -d "1abc123..." --range "Introduction" -l ja
  gdocumentor summary -d "1abc123..." --range "@100:600" -l ja
`
	fmt.Fprint(w, text)
}

func printDraftHelp(w io.Writer) {
	text := `gdocumentor draft — Grounded Initial Draft Generator (Google Docs & Local Mode)
Generate a structured initial draft from a topic/outline prompt (-p), grounded in optional reference files (-c),
RAG rulebooks (-r / --drive-rag), and custom AI Persona (--persona), and insert it into a newly created Pageless
Google Document, an existing blank Google Document (-d), or a local file (--no-auth / -o).

USAGE:
  gdocumentor draft -p "<topic or outline>" [flags]
  gdocumentor draft --no-auth -p "<topic or outline>" -o <draft.md> [flags]

FLAGS:
  -p, --prompt <text>           Topic outline and instructions for generating the initial draft (required unless --generated-draft is set)
      --title <string>          Title for the newly created Google Document
  -d, --doc-id <id|url>         Optional existing blank Google Document ID or URL to populate
      --parent-folder <id|url>  Optional Google Drive Folder ID or URL for the new document
      --tab-id <id>             Optional target tab ID within the Google Document
  -c, --context <path|url>      Reference context file(s), directory, or URLs to ground the draft
  -r, --rules <path|dir|url>    RAG rulebook file(s), directory, or URL (templates, style guides, persona)
      --rules-dir <dir>         Directory of local RAG rulebook files
      --drive-rag <id|url>      Google Drive Folder/File ID or URL containing organization templates/rulebooks
      --persona <text>          Custom AI writer/coach persona
  -l, --lang <code>             Draft language: "en" (default), "ja", "de", "fr", "es", "zh", "ko", "auto", etc.
      --model <name>            Gemini model in standalone CLI mode (default: "gemini-3.8-flash"; also "gemini-4-hydrogen", "gemini-4-carbon", "gemini-4-argon")
      --no-auth, --local        Generate draft without Google OAuth2 and save directly to a local file
  -o, --output <file-path>      Save the generated draft to a local file (works in BOTH --no-auth and Google Docs modes)
      --save-local              Also save the generated draft to gdocumentor_draft.md alongside Google Docs
      --generated-draft <txt>   Insert pre-generated draft text (or @file) without calling internal Gemini API (0 tokens)
      --json                    Output pure JSON to stdout

EXAMPLES:
  # 1. Generate a new Google Document draft from a prompt and writing guidelines rulebook
  gdocumentor draft --title "OAuth2 Design RFC" -r sample/rules/writing_guidelines.md -p "Propose a hybrid OAuth2 token cache in Go." -l ja

  # 2. Generate a draft grounded in local source code (-c) and save to BOTH Google Docs and a local file (-o)
  gdocumentor draft --title "CLI Architecture Overview" -c internal/cli/app.go -p "Write an architecture overview of the CLI." -o draft_overview.md

  # 3. Access-Token-Free Local Draft Generation (--no-auth)
  gdocumentor draft --no-auth -r sample/rules/ -p "Write a tutorial on UTF-16 indexing in Google Docs." -o tutorial_draft.md -l ja
`
	fmt.Fprint(w, text)
}

func printEditHelp(w io.Writer) {
	text := `gdocumentor edit — Direct 0-Token Google Docs Editor (Text, Rich Markdown, Tables & Images)
Directly create or delete a Google Document, insert plain text or rich Markdown (headings, bold/italic/code/links,
lists, tables, and local/remote images) at any position, replace text or ranges, delete ranges, or insert/replace/resize/delete
images (VISUAL_1, etc.) without consuming any Gemini API tokens (0 tokens).

USAGE:
  gdocumentor edit --create-doc --title "<Title>" [flags]
  gdocumentor edit -d <doc-id-or-url> [insertion / replacement / deletion / image flags]

DOCUMENT CREATION & DELETION FLAGS:
      --create-doc, --create    Create a new Pageless Google Document (combine with --title, --parent-folder, and optional -i / --text)
      --delete-doc              Permanently delete the target Google Document from Google Drive
  -d, --doc-id <id|url>         Target Google Document ID or full URL
      --tab-id <id>             Target tab ID within a multi-tab Google Document

TEXT & RICH MARKDOWN INSERTION FLAGS:
      --text, --insert <str>    Text or Markdown content to insert (or pass via -i <file>, pipe '|', '<<<', '<<EOF', or '< file')
  -i, --input <file>            Read content to insert/replace from a local file (.md files auto-enable --markdown)
      --markdown, --md          Convert Markdown syntax (headings, bold/italic/code/links, lists, tables, images) to native Google Docs elements
      --raw-text                Insert .md file content as literal plain text without Markdown conversion
      --prepend, --start        Insert at the very beginning of the document body (index 1)
      --append, --end           Insert at the end of the document body (default when inserting into an existing Doc)
      --at <spec>               Insert at exact UTF-16 offset ("@150"), 1-based paragraph number ("3"), "start", or "end"
      --index <num>             Insert at exact 1-based UTF-16 character index ("150" or "@150")
      --before <phrase>         Insert immediately before the matching text/heading phrase
      --after <phrase>          Insert immediately after the matching text/heading phrase
      --before-paragraph <N>    Insert before 1-based paragraph number N
      --after-paragraph <N>     Insert after 1-based paragraph number N
      --style <style>           Apply paragraph style ("HEADING_1".."HEADING_6", "TITLE", "SUBTITLE", "NORMAL_TEXT")
      --suggest                 Apply text insertion, replacement, or deletion as a native Google Docs suggestion (WriteMode.SUGGEST)

FIND, REPLACE & DELETE FLAGS:
      --find <phrase>           Search text/phrase to replace (combine with --replace, --text, -i <file>, or stdin)
      --replace <str>           Replacement text for --find or --replace-range
      --replace-range <spec>    Replace a specific range ("@100:250", "2-4", "all", or heading phrase)
      --replace-body            Replace the entire body content of the target Google Document tab
      --delete-range <spec>     Delete a specific range ("@100:250", "3", "2-5", or "all")
      --delete-text <phrase>    Delete occurrences of a specific text substring
      --clear                   Clear all body content in the target Google Document tab
      --ignore-case             Case-insensitive matching for --find, --delete-text, --before, and --after
      --occurrence <N|all>      Target only the N-th occurrence (1-based) or "all" (0)

IMAGE INSERTION, REPLACEMENT, RESIZING & DELETION FLAGS:
      --insert-image <src>      Insert a local image (.png/.jpg/.gif/.webp) or image URL at the target position
      --replace-image <id>      Replace an existing image ("VISUAL_1", "1", objectId, "@index", or "all") with --with-image
      --resize-image <id>       Resize an existing image ("VISUAL_1", "1", objectId, "@index", or "all") in place
      --with-image <src>        Replacement local image file path or URL for --replace-image
      --delete-image <id>       Delete an existing image ("VISUAL_1", "1", objectId, "@index", or "all")
      --image-width <pt>        Target width in points (PT) for inserted, replaced, or resized images (aspect ratio preserved if height omitted)
      --image-height <pt>       Target height in points (PT) for inserted, replaced, or resized images

EXAMPLES:
  # 1. Create a new Google Doc from a rich Markdown file (headings, tables, lists, and local images converted natively)
  gdocumentor edit --create-doc --title "Project Report" -i sample/sample_article.md

  # 2. Insert rich Markdown (tables, lists, headings) after a specific heading or paragraph
  gdocumentor edit -d "1abc123..." --after "Introduction" --markdown -i new_section.md
  gdocumentor edit -d "1abc123..." --prepend <<< "Inserted at the very top via here-string"
  echo "Appended via pipe" | gdocumentor edit -d "1abc123..." --append

  # 3. Find & replace text, replace paragraph ranges, or delete ranges
  gdocumentor edit -d "1abc123..." --find "draft version" --replace "v1.0.0 release"
  gdocumentor edit -d "1abc123..." --replace-range "2-3" -i updated_paragraphs.txt
  gdocumentor edit -d "1abc123..." --delete-range "4"

  # 4. Insert, replace, resize in place, or delete images (VISUAL_1, etc.)
  gdocumentor edit -d "1abc123..." --insert-image ./images/diagram.png --after "Architecture" --image-width 420
  gdocumentor edit -d "1abc123..." --replace-image VISUAL_1 --with-image ./images/updated_diagram.png --image-width 400
  gdocumentor edit -d "1abc123..." --resize-image VISUAL_1 --image-width 300
  gdocumentor edit -d "1abc123..." --delete-image VISUAL_2
`
	fmt.Fprint(w, text)
}

func printManageHelp(w io.Writer) {
	text := `gdocumentor manage — Batch Suggestion Acceptance/Rejection, Comment Cleanup & Document Lifecycle
Accept or reject all pending inline suggestions on a Google Document, delete open comment threads, reset the
Coach Impact & Decision Record, or create/delete a Google Document without consuming any Gemini API tokens (0 tokens).

USAGE:
  gdocumentor manage -d <google-doc-id-or-url> [action flags]
  gdocumentor manage --create-doc --title "<Title>"

ACTION FLAGS:
  -d, --doc-id <id|url>         Target Google Document ID or full URL (required unless --create-doc is set)
      --tab-id <id>             Optional target tab ID within a multi-tab Google Document
      --accept-all              Accept all pending inline suggestions in the document and update the Decision Record
      --reject-all              Reject (clear) all pending inline suggestions in the document and update the Decision Record
      --delete-comments         Delete all active comment threads on the document via Google Drive Comments API
      --reset-record            Reset the Coach Impact & Decision Record statistics for the document
      --create-doc              Create a new blank Pageless Google Document on Google Drive
      --title <string>          Title when creating a new Google Document with --create-doc
      --parent-folder <id|url>  Optional Google Drive Folder ID/URL when creating a new Google Document
      --delete-doc              Permanently delete the Google Document from Google Drive and purge its Decision Record
  -l, --lang <code>             Output language: "en" (default), "ja", "de", "fr", "es", "zh", "ko", "auto", etc.
      --json                    Output pure JSON to stdout

EXAMPLES:
  # 1. Accept all pending inline suggestions on a Google Document
  gdocumentor manage -d "https://docs.google.com/document/d/1abc123.../edit" --accept-all

  # 2. Reject all pending inline suggestions AND delete all open comments in a single command
  gdocumentor manage -d "1abc123..." --reject-all --delete-comments

  # 3. Reset the Coach Impact & Decision Record for a document
  gdocumentor manage -d "1abc123..." --reset-record

  # 4. Permanently delete a test Google Document from Google Drive
  gdocumentor manage -d "1abc123..." --delete-doc
`
	fmt.Fprint(w, text)
}

func printInspectHelp(w io.Writer) {
	text := `gdocumentor inspect — Structural Paragraph, UTF-16 Coordinate & Review Thread Inspector
Inspect an existing Google Document to list 1-based paragraph/block numbers (#1, #2...), exact UTF-16 character
offset ranges (@start:end), tables (TABLE_1...), visual elements (VISUAL_1...), pending inline suggestions
(with 🤖 AI Coach vs 👤 Human provenance), open comment threads, and the Coach Impact & Decision Record.
Consumes zero Gemini API tokens (0 tokens).

USAGE:
  gdocumentor inspect -d <google-doc-id-or-url> [flags]

FLAGS:
  -d, --doc-id <id|url>         Target Google Document ID or full URL (required)
      --tab-id <id>             Optional target tab ID within a multi-tab Google Document (e.g. "t.0")
  -l, --lang <code>             Output language: "en" (default), "ja", "de", "fr", "es", "zh", "ko", "auto", etc.
      --json                    Output full structured document inspection JSON to stdout

EXAMPLES:
  # 1. Inspect paragraph numbers (#1, #2...), UTF-16 (@start:end) coordinates, and active review threads
  gdocumentor inspect -d "https://docs.google.com/document/d/1abc123.../edit" -l ja

  # 2. Inspect a specific tab and export structured JSON for scripting or AI agents
  gdocumentor inspect -d "1abc123..." --tab-id "t.0" --json | jq '.inspectedDoc.editableBlocks'
`
	fmt.Fprint(w, text)
}

func printMCPHelp(w io.Writer) {
	text := `gdocumentor mcp — Dual-Engine Model Context Protocol (MCP) JSON-RPC 2.0 Server
Start the Model Context Protocol (MCP) server over stdio for any MCP-compatible AI client (Google Antigravity,
Claude Desktop, Claude Code, Cursor, VS Code / GitHub Copilot, Windsurf, Gemini CLI).

HOW HOST-AI MODE WORKS (ZERO GEMINI_API_KEY REQUIRED):
  • By default, 'gdocumentor mcp' runs in Host-AI (Agent-Side LLM) Mode: your AI client (Claude, GPT, Gemini, Cursor)
    performs all proofreading, coaching rationale generation, learning summary synthesis, and drafting using its own LLM,
    while gdocumentor handles local/remote workspace resolution, AST dependency discovery (--auto-deps), RAG rulebook
    loading, exact UTF-16 surrogate-pair indexing, and Google Docs/Drive API batch updates!
  • Works both WITH Google Docs (via OAuth2 / gcloud ADC) AND WITHOUT Google Docs (by passing "noAuth": true to output
    inline diff or Socratic hint reports directly to local Markdown files).
  • Optional Internal Gemini Mode: Set environment variable DOCUMENTOR_MCP_USE_INTERNAL_GEMINI=1 (plus GEMINI_API_KEY)
    if you want the MCP server to call the Gemini API internally instead of using the Host AI's LLM.

EXPOSED MCP TOOLS (6 TOOLS):
  • gdocumentor_proofread   Proofread local files/code/URLs or Google Docs (Suggest Mode or Socratic Hint-Only Mode)
  • gdocumentor_summary     Analyze existing suggestions/comments on a Google Doc and generate a Learning Summary
  • gdocumentor_inspect     Inspect paragraph numbers (#1, #2...), UTF-16 (@start:end) coordinates, and VISUAL_1/TABLE_1 IDs
  • gdocumentor_draft       Generate a grounded initial draft into a new/blank Google Doc or local file
  • gdocumentor_edit        Direct 0-token Google Docs editing (create/delete doc, insert Markdown/tables/images, resize VISUAL_1)
  • gdocumentor_manage      Batch accept/reject inline suggestions, delete comments, reset Decision Record, or delete Doc

USAGE:
  gdocumentor mcp
  gdocumentor setup --help    (to automatically register 'gdocumentor mcp' in your AI clients)

EXAMPLES:
  # 1. Automatically register 'gdocumentor mcp' across your AI clients (see 'gdocumentor setup --help')
  gdocumentor setup --all
  gdocumentor setup --agent claude-desktop --wsl-host

  # 2. Test the MCP JSON-RPC 2.0 server handshake and tool listing from your terminal
  printf '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}\n{"jsonrpc":"2.0","id":2,"method":"tools/list"}\n' | gdocumentor mcp | jq .
`
	fmt.Fprint(w, text)
}
