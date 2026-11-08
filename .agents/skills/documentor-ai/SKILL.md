---
name: documentor-ai
description: Proofread and review local manuscripts, multi-file source code, specific line/character ranges, or existing Google Documents, or directly create, edit, and inspect Google Documents (including rich Markdown, tables, and image insertion/replacement/resizing) using gdocumentor (DocuMentor AI Go CLI & MCP Server). Creates or updates Google Docs with native inline Suggested Edits (WriteMode.SUGGEST), educational rationale replies, Socratic Hint-Only coaching comments, multimodal figure/chart/table reviews, RAG organization rule enforcement, direct 0-token document/Markdown/image editing, and a structured Writing Coach & Learning Summary. Activate this skill whenever the user wants to proofread documents/code into Google Docs, review an existing Google Document, summarize review threads on a Google Doc, generate a Google Doc draft, directly create/edit/inspect a Google Doc (insert Markdown/tables/images or resize VISUAL_1), or accept/reject/clean up Google Docs suggestions and comments.
---

# gdocumentor (DocuMentor AI) — Multi-Agent Skill (Antigravity, Claude Desktop, Claude Code, Cursor, VS Code / GitHub Copilot, Windsurf, Gemini CLI)

**gdocumentor (`DocuMentor AI`)** transforms AI from a passive "ghostwriter" into an interactive **Writing & Code Review Coach** built directly on top of Google Docs' native **Suggesting Mode (`WriteMode.SUGGEST`)** and **Anchored Comments (`insertComment` / `addCommentReply`)**. It works across **Google Antigravity (`agy`)**, **Claude Desktop**, **Claude Code**, **Cursor**, **VS Code (GitHub Copilot)**, **Windsurf**, and **Gemini CLI** on **Linux, macOS, Windows, and WSL2** (including Windows GUI apps calling `gdocumentor` inside WSL2).

Instead of silently overwriting a user's manuscript or code, DocuMentor AI:
1. Natively converts single-file **Markdown (`.md`, `.markdown`, automatically embedding local/remote images as Base64 Data URIs with a Drive `thumbnailLink` `=s2048` fallback), Text (`.txt`), PDF (`.pdf`), and DOCX (`.docx`)** documents into Google Docs (`application/vnd.google-apps.document`), inserts source code / script files (`.go`, `.js`, `.ts`, `.py`, `.gs`, `.sh`, etc.) directly as plain text into a newly created Google Document, or reads an existing Google Document tab (`-d`, including pending suggestions, open comments, resolved review history, tables, and embedded `contentUri` / `sourceUri` figures/charts).
2. Automatically resolves local dependency files (`--auto-deps` for Go, JS/TS, Python, C/C++, Rust, Markdown links, or adjacent numbered chapters), organization RAG rulebooks (`--rules`, `--rules-dir`, `--drive-rag`), and custom **AI Persona / Positioning** (`# AIの位置づけ` / `# AI Persona` inside RAG rulebooks or via `--persona` / `--role`) in parallel.
3. Calls **Gemini** (`gemini-3.8-flash` by default, or `gemini-4-argon` for deep multi-file reasoning) with hybrid context optimization (zero-latency inline XML tags for text files; parallel Gemini File API resumable upload with guaranteed `defer` deletion for PDFs and embedded images).
4. Maps every suggestion to exact **UTF-16 code unit coordinates** (including surrogate pairs for emojis/CJK) and applies them in **bottom-to-top (`StartIndex` descending)** order so earlier indices never shift.
5. Attaches an educational **Reason for Revision (`💡`)** and **Learning Point (`🎓`)** to every inline suggestion card, and returns a structured **Writing Coach & Learning Summary** plus the clickable **Google Docs URL**.

---

## When to Activate This Skill

Activate `documentor-ai` when the user asks to:
- Proofread or review a local text/Markdown manuscript, single source file, multiple files, an entire directory, a **Web article URL (`https://...`)**, or a **GitHub repository/file URL** and review the results in **Google Docs**.
- Proofread only a specific line range (e.g., `main.go:15-45` or `https://github.com/owner/repo/blob/main/src/code.js#L15-L45`) or character offset range (e.g., `article.md@100:250` or `https://example.com/post@100:500`) while automatically checking cross-file dependencies and style guides.
- Proofread an **existing Google Document** (by Document ID or URL), optionally scoped to a specific tab (`--tab-id`) or character range (`--range`).
- Provide **Socratic Hint-Only Coaching (`--mode hint`)** where the AI highlights issues and gives guiding hints via comments so the author can rewrite the text themselves.
- Synthesize a **Writing Coach & Learning Summary (`summary` subcommand)** from existing reviewer suggestions and comments (including resolved/accepted/rejected threads) on a Google Document.
- Generate an **initial draft (`draft` subcommand)** directly into a new or blank Google Document based on a topic prompt and RAG rulebooks.
- **Directly create, edit, or inspect a Google Document (`edit` / `inspect` subcommands — 0 Gemini tokens)**: insert plain text or converted rich Markdown (headings, bold/italic/code/links, lists, native tables, and local/remote images) at any paragraph or phrase, or insert/replace/resize/delete images (`VISUAL_1`, etc.).
- **Accept all suggestions, reject all suggestions, or delete comments (`manage` subcommand)** on a Google Document.

---

## Prerequisite Environment Check

1. **Dual AI Execution Architecture (MCP Server Mode vs. Standalone CLI Mode)**:
   - **MCP Server Mode (`gdocumentor_server` via `call_mcp_tool`)**: Does **NOT** require `GEMINI_API_KEY`! When called via MCP, **YOU (the client AI agent)** perform the proofreading, learning summary, and draft generation using your own LLM (`aiResult`, `aiSummaryResult`, `generatedDraft`), while `gdocumentor` handles all deterministic file/RAG/dependency resolution, UTF-16 coordinate matching, and Google Docs API automation.
   - **Standalone CLI Mode (`gdocumentor` in terminal)**: Uses the internal Gemini API and requires `GEMINI_API_KEY` (unless `--prepare-only` / `--ai-result` is passed).
2. **Google Workspace OAuth2 Credentials (Required for Google Docs & Drive mode; NOT required when using `noAuth: true` / `--no-auth`)**: Application Default Credentials (`~/.config/gcloud/application_default_credentials.json`) or `gcloud` CLI authentication with `documents` and `drive` scopes. When the user requests `noAuth: true` (`--no-auth` / `--local`), neither Google OAuth2 nor `GEMINI_API_KEY` is needed in MCP mode, and results are saved directly to `outputPath` in the workspace.

---

## How to Execute the Tool (MANDATORY: Default to MCP Server; Use CLI Tool ONLY When Explicitly Requested in Prompt!)

> **🚨 CRITICAL EXECUTION RULE: DEFAULT TO THE MCP SERVER (`gdocumentor_server`) — USE THE CLI BINARY ONLY WHEN EXPLICITLY INSTRUCTED IN THE PROMPT!**
> 1. **Default Behavior (MCP Server + Host-AI Engine — `GEMINI_API_KEY` NOT Required)**:
>    - Whenever `gdocumentor_server` (or `gdocumentor`) is registered as an MCP server, you **MUST** execute `gdocumentor` via its MCP tools (`gdocumentor_proofread`, `gdocumentor_summary`, `gdocumentor_inspect`, `gdocumentor_draft`, `gdocumentor_edit`, `gdocumentor_manage`), **unless the user's prompt explicitly states to use `gdocumentor` as a CLI tool rather than as an MCP server**:
>      - **In Claude Desktop, Claude Code, Cursor, VS Code (GitHub Copilot), Windsurf, and Gemini CLI**: Call the native MCP tool directly (`gdocumentor_proofread`, `gdocumentor_summary`, `gdocumentor_inspect`, `gdocumentor_draft`, `gdocumentor_edit`, `gdocumentor_manage`) with the JSON arguments.
>      - **In Google Antigravity (`agy`)**: Call the MCP tool via **`call_mcp_tool`** (`ServerName: "gdocumentor_server"` or `"gdocumentor"`, `ToolName: "gdocumentor_proofread"`, etc.).
>    - **DO NOT** run `scripts/run_documentor.sh`, `scripts/run_documentor.cmd`, or `gdocumentor` via shell/terminal by default, **even if the user's prompt mentions CLI flag names like `--no-auth`, `--hint`, `--summary-tab`, `--auto-deps`, `-o`, or `-r`**. Instead, translate those flags into the MCP tool's JSON arguments (see the mapping table below) and invoke the MCP tool!
> 2. **Explicit CLI-Tool Exception (`GEMINI_API_KEY` Required)**:
>    - **ONLY** when the user explicitly instructs in their prompt to run `gdocumentor` **as a CLI tool (not via the MCP server)** (e.g., *"Use gdocumentor as a CLI tool rather than an MCP server..."* / *"gdocumentorをMCPサーバーとしてではなく、CLIツールとして使用して..."*), run the `gdocumentor` CLI binary via your shell/command tool (which uses the internal Gemini API with `GEMINI_API_KEY`).
> 3. **Workspace, Cross-Platform Path Normalization (Windows ↔ WSL2), & Host-AI Rules**:
>    - Always pass `"workDir"` set to the user's active workspace directory (or use paths inside the user's workspace) so all input/output files are resolved and saved cleanly inside the user's current working directory (never write files to `/tmp` unless explicitly asked). Windows drive paths (`C:\Users\...`), WSL UNC paths (`\\wsl.localhost\Ubuntu\...`), and Linux/WSL paths (`/mnt/c/...`, `/home/...`) are automatically normalized across platforms.
>    - **HOST-AI EXECUTION IN MCP MODE (`aiResult` / `aiSummaryResult` / `generatedDraft`)**:
>      - **1-Step Fast Path (Recommended for Local Workspace Files)**: Read the target file(s) (and any `-r` rulebooks or local images), generate the `"aiResult"` JSON (`overallFeedback`, `learningSummary`, `existingSuggestionsNote`, `suggestions`, `elementSuggestions` where each `originalText` is an exact single-line substring from the target text) using your own LLM, and pass `"aiResult"` directly in a **single** `gdocumentor_proofread` call!
>      - **2-Step Universal Path (For Existing Google Docs `docId`, Remote URLs, PDFs/DOCX, or `--auto-deps`)**: Call `gdocumentor_proofread` first without `aiResult` (or with `"prepareOnly": true`) to receive `preparedContext` (`documentId`, `editableBlocks`, `tables`, `visualElements`, `coachingPrompt`), generate `"aiResult"` using your own LLM, and call `gdocumentor_proofread` a second time with `"docId": "<documentId>"`, `"skipDocInsert": true`, and `"aiResult": { ... }`!

### CLI Flag → MCP `Arguments` Translation Table
| User Prompt / CLI Flag | MCP Tool & JSON `Arguments` Field |
| :--- | :--- |
| `--no-auth` / `--local` | `"noAuth": true` (`gdocumentor_proofread`, `gdocumentor_draft`) |
| `-i <path>` / `--input <path>` | `"inputs": ["<path>"]` |
| `-t <file:start-end>` / `--target` | `"targets": ["<file:start-end>"]` |
| `--repo <github-url>` | `"repoUrl": "<github-url>"` |
| `--range <spec>` | `"range": "<spec>"` |
| `-c <path>` / `--context <path>` | `"contexts": ["<path>"]` |
| `--auto-deps` / `--no-auto-deps` | `"autoDeps": true` / `false` |
| `-r <path>` / `--rules <path>` | `"rules": ["<path>"]` |
| `--rules-dir <dir>` | `"rulesDir": "<dir>"` |
| `--drive-rag <id>` | `"driveRag": "<id>"` |
| `--persona <text>` | `"persona": "<text>"` |
| `-d <docId>` / `--doc-id <docId>` | `"docId": "<docId>"` |
| `--title <title>` | `"title": "<title>"` |
| `--summary-tab` | `"summaryTab": true` |
| `-m hint` / `--hint` | `"mode": "hint"` |
| `-m suggest` | `"mode": "suggest"` |
| `-l ja` / `-l en` | `"lang": "ja"` / `"en"` |
| `-p <prompt>` / `--prompt` | `"prompt": "<prompt>"` |
| `-o <file>` / `--output <file>` | `"outputPath": "<file>"` |
| `--save-local` | `"saveLocal": true` |

### Available MCP Tools (`ServerName: "gdocumentor_server"`)
- **`gdocumentor_proofread`**: Proofread local files/directories (`.md`, `.txt`, `.pdf`, `.docx`, or source code scripts), line/character ranges (`targets`, `range`), GitHub repos (`repoUrl`), Web URLs, or existing Google Docs (`docId`) with RAG (`rules`, `rulesDir`, `driveRag`) and custom AI Persona (`persona`). Supports both Google Docs mode and Access-Token-Free Local Mode (`noAuth: true` + `outputPath`).
- **`gdocumentor_summary`**: Read an existing Google Doc (`docId`) and generate a standalone Writing Coach & Learning Summary + Coach Impact & Decision Record (optionally creating a Summary Tab via `summaryTab: true` or saving locally via `outputPath`).
- **`gdocumentor_inspect`**: Inspect an existing Google Doc (`docId`) for 1-based paragraph numbers (`#1`, `#2`...), exact UTF-16 coordinates (`@start:end`), embedded images/charts (`VISUAL_1`, `objectId`), native tables (`TABLE_1`), pending `🤖 AI` / `👤 Human` suggestions, and comments (0 Gemini tokens).
- **`gdocumentor_draft`**: Generate an initial draft into a new (Pageless) or blank Google Doc (or local file via `noAuth: true` / `outputPath`) from `prompt`.
- **`gdocumentor_edit`**: Directly create or delete a Google Doc (`createDoc`, `deleteDoc`), insert plain text or **richly formatted Markdown** (`markdown: true`, converting headings, bold/italic/strikethrough/inline code, links, nested lists, blockquotes, code blocks, horizontal rules, native tables, and local/remote images) at any location (`position`: `"start"`/`"end"`, `at`: `"@150"`/`"3"`, `before`, `after`, `beforeParagraph`, `afterParagraph`), **insert/replace/resize/delete images** (`insertImage`, `replaceImage` / `resizeImage` + optional `withImage`, `deleteImage`, `imageWidth`, `imageHeight`), replace text/ranges/full body (`find` + `replace`, `replaceRange`, `replaceBody`), or delete ranges/text (`deleteRange`, `deleteText`, `clearBody`) with zero Gemini tokens.
- **`gdocumentor_manage`**: Create a new blank Google Doc (`createDoc`), accept/reject all suggestions (`acceptAll`, `rejectAll`), delete comments (`deleteComments`), reset Decision Record (`resetRecord`), or delete a Google Doc (`deleteDoc`).

---

### 1. Access-Token-Free Local Mode (`noAuth: true` — `GEMINI_API_KEY`: Not Required in MCP Mode / Google OAuth2 Access Token: Not Required)
When the user asks to proofread without an OAuth access token (`--no-auth` / `noAuth: true`) and save to a local file in the workspace:
```json
// Tool Call: call_mcp_tool
{
  "ServerName": "gdocumentor_server",
  "ToolName": "gdocumentor_proofread",
  "Arguments": {
    "workDir": "/absolute/path/to/current/workspace",
    "noAuth": true,
    "inputs": ["./sample/sample_article.md"],
    "outputPath": "./article_coaching.md",
    "lang": "en",
    "aiResult": { ... }
  }
}
```

### 2. Proofread Local File(s) into a New Pageless Google Document (`GEMINI_API_KEY`: Not Required in MCP Mode / Google OAuth2 Access Token: Required)
```json
// Tool Call: call_mcp_tool
{
  "ServerName": "gdocumentor_server",
  "ToolName": "gdocumentor_proofread",
  "Arguments": {
    "workDir": "/absolute/path/to/current/workspace",
    "inputs": ["./sample/sample_article.md"],
    "persona": "You are a supportive university writing professor.",
    "summaryTab": true,
    "outputPath": "./interactive_coach_report.md",
    "lang": "en",
    "aiResult": { ... }
  }
}
```

### 3. Socratic Hint-Only Mode (`mode: "hint"` / `--hint` — `GEMINI_API_KEY`: Not Required in MCP Mode / Google OAuth2 Access Token: Required for Google Docs or Not Required with `noAuth: true`)
Attaches anchored coaching comments (`insertComment`) with the flagged phrase, issue explanation (`💡`), and rewriting hint (`🎓`)—without revealing the rewritten answer:
```json
// Tool Call: call_mcp_tool
{
  "ServerName": "gdocumentor_server",
  "ToolName": "gdocumentor_proofread",
  "Arguments": {
    "workDir": "/absolute/path/to/current/workspace",
    "inputs": ["./sample/sample_article.md"],
    "rules": ["./sample/rules/writing_guidelines.md"],
    "mode": "hint",
    "persona": "You are a supportive university writing professor. Encourage critical thinking without giving away the rewritten sentence.",
    "title": "Student Draft - Socratic Coaching",
    "summaryTab": true,
    "outputPath": "./student_socratic_hints.md",
    "lang": "en"
  }
}
```

### 4. Proofread a Specific Script / Function Range in a Local Directory or Remote GitHub Repository (`targets` + `autoDeps: true` — `GEMINI_API_KEY`: Not Required; Google OAuth2 Access Token: Required unless `noAuth: true`)
```json
// A. Local Script Line Range with Automatic Dependency Resolution:
{
  "ServerName": "gdocumentor_server",
  "ToolName": "gdocumentor_proofread",
  "Arguments": {
    "workDir": "/absolute/path/to/current/workspace",
    "targets": ["./demo_script.js:1-20"],
    "rules": ["./sample/rules/coding_standards.md"],
    "autoDeps": true,
    "outputPath": "./code_review_report.md",
    "prompt": "Focus on error handling, security, and modern JavaScript best practices.",
    "lang": "en"
  }
}

// B. Remote GitHub Blob URL (#Lstart-Lend) or Repository (repoUrl) with Auto-Deps:
{
  "ServerName": "gdocumentor_server",
  "ToolName": "gdocumentor_proofread",
  "Arguments": {
    "workDir": "/absolute/path/to/current/workspace",
    "targets": ["https://github.com/tanaikech/DocuMentorAI/blob/main/src/code.js#L1-L35"],
    "autoDeps": true,
    "outputPath": "./github_code_review.md",
    "lang": "en"
  }
}
```

### 5. Direct Google Docs, Rich Markdown & Image Editing (`gdocumentor_edit` — `GEMINI_API_KEY`: Not Required (0 Tokens); Google OAuth2 Access Token: Required)
```json
// A. Create a new Google Document with initial text:
{
  "ServerName": "gdocumentor_server",
  "ToolName": "gdocumentor_edit",
  "Arguments": {
    "createDoc": true,
    "title": "Project Status Report",
    "text": "Paragraph 1: Introduction\nParagraph 2: Conclusion"
  }
}

// B. Insert a Markdown file (with headings, bold/italic/code/links, lists, tables, and local images) before Paragraph 2:
{
  "ServerName": "gdocumentor_server",
  "ToolName": "gdocumentor_edit",
  "Arguments": {
    "workDir": "/absolute/path/to/current/workspace",
    "docId": "<DOC_ID>",
    "inputs": ["./sample/sample_article.md"],
    "beforeParagraph": 2,
    "markdown": true
  }
}

// C. Resize an existing image (VISUAL_1) in place (withImage is optional when resizing) or replace it with a new file:
{
  "ServerName": "gdocumentor_server",
  "ToolName": "gdocumentor_edit",
  "Arguments": {
    "workDir": "/absolute/path/to/current/workspace",
    "docId": "<DOC_ID>",
    "replaceImage": "VISUAL_1",
    "imageWidth": 300
  }
}
```

### 6. Inspect (`gdocumentor_inspect`), Summarize (`gdocumentor_summary`), Draft (`gdocumentor_draft`), & Manage (`gdocumentor_manage`) (`GEMINI_API_KEY`: Not Required; Google OAuth2 Access Token: Required)
```json
// Inspect paragraph numbers (#1, #2...), UTF-16 ranges (@start:end), VISUAL_N, and TABLE_N (0 Gemini tokens):
{
  "ServerName": "gdocumentor_server",
  "ToolName": "gdocumentor_inspect",
  "Arguments": {
    "docId": "<DOC_ID>",
    "lang": "en"
  }
}

// Accept all pending suggestions and update the Coach Impact & Decision Record:
{
  "ServerName": "gdocumentor_server",
  "ToolName": "gdocumentor_manage",
  "Arguments": {
    "docId": "<DOC_ID>",
    "acceptAll": true
  }
}
```

---

### Fallback / Explicit CLI-Tool Mode (`scripts/run_documentor.sh` or `gdocumentor` — ONLY When Prompt Explicitly Requests CLI Tool Instead of MCP Server, or When MCP Server Is Unavailable)
**Only** when the user's prompt explicitly asks to execute `gdocumentor` **as a CLI tool rather than via the MCP server** (requiring `GEMINI_API_KEY`), or if neither `gdocumentor_server` nor `gdocumentor` is registered in `<mcp_servers>`, run `gdocumentor` (or `<path-to-skill-dir>/scripts/run_documentor.sh` with `--json`) via `run_command`:
```bash
"<path-to-skill-dir>/scripts/run_documentor.sh" proofread -w "$PWD" -i "./sample/sample_article.md" --json
```

---

## Presenting Results to the User

After parsing the JSON output from `stdout` (see [`references/json_schema.md`](references/json_schema.md) for the complete schema):
1. **Present the clickable Google Document URL (`documentUrl`) and/or Local Output File (`outputFilePath`)**:
   - When an Access Token is used (`localMode: false`), always present the clickable Google Document URL (`documentUrl`) so the user can open Google Docs and accept/reject each inline suggestion graphically. If `-o <file>` / `--save-local` (`"outputPath"` / `"saveLocal": true`) was also specified, present `outputFilePath` as well.
   - When running without an Access Token (`--no-auth` / `"noAuth": true`, `localMode: true`), read or reference the generated local Markdown file (`outputFilePath`) to clearly explain the inline revisions (`-`/`+` diffs) or Socratic coaching hints (`--hint`) to the user.
2. **Present the `Writing Coach & Learning Summary`**:
   - **Overall Feedback (`overallFeedback`)**
   - **Revisions Digest (`learningSummary.revisionsDigest`)**
   - **Observed Habits & Weaknesses (`learningSummary.weakPointsAndHabits`)**
   - **Actionable Tips for Future Writing/Coding (`learningSummary.actionableTips`)**
3. **Summarize key matched suggestions (`matchedSuggestions` / `matchedElementSuggestions`)**, including file/line coordinates (`filePath`, `lineNumber`), category, original vs. suggested text, reason (`💡`), and learning point (`🎓`).
