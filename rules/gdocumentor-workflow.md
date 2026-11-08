---
trigger: model_decision
description: >-
  Activate when the user wants to proofread, review, summarize, inspect, draft, manage, or directly create/edit
  a Google Document (including inserting rich Markdown, tables, or images, and replacing/resizing images like VISUAL_1),
  or review a manuscript, source script, partial function/line range in a local directory, remote GitHub repository,
  or Web URL using gdocumentor.
---

# gdocumentor (DocuMentor AI) MCP-First & Context-Aware Workflow Rule

Whenever you use `gdocumentor` to proofread, summarize, inspect, draft, manage, or directly edit a manuscript, source script, or Google Document:

1. **MANDATORY: Default to the Bundled MCP Server (`gdocumentor_server` / Host-AI Engine — 0 `GEMINI_API_KEY`) unless CLI Execution is Explicitly Requested**:
   - **Default (MCP Server Mode)**: Unless the user's prompt **explicitly instructs you to run `gdocumentor` as a CLI tool (not as an MCP server)**, you **MUST** invoke the `gdocumentor_server` MCP tools (`gdocumentor_proofread` / `gdocumentor_summary` / `gdocumentor_inspect` / `gdocumentor_draft` / `gdocumentor_edit` / `gdocumentor_manage` — called directly in Claude Desktop, Claude Code, Cursor, VS Code/Copilot, Windsurf, and Gemini CLI, or via `call_mcp_tool` with `ServerName: "gdocumentor_server"` in Google Antigravity).
   - **Do NOT execute `run_documentor.sh`, `run_documentor.cmd`, or `gdocumentor` via shell/terminal by default**, even if the user prompt mentions CLI flag names like `--no-auth`, `--hint`, `--summary-tab`, `--auto-deps`, `-o`, or `-r`. Instead, translate those flags into MCP JSON `Arguments` (`noAuth: true`, `mode: "hint"`, `summaryTab: true`, `autoDeps: true`, `outputPath`, `rules`, `workDir`, etc.).
   - **Explicit CLI-Tool Exception (`GEMINI_API_KEY` Mode)**: **ONLY** when the user explicitly states in their prompt to run `gdocumentor` **as a CLI tool rather than via the MCP server** (e.g., *"Use gdocumentor as a CLI tool (not via MCP)..."* / *"MCPサーバーとしてではなく、CLIツールとしてgdocumentorを実行して..."*), execute the `gdocumentor` binary via shell/terminal (which uses the internal Gemini API with `GEMINI_API_KEY`), or pass `"useInternalGemini": true` if they explicitly request the internal Gemini API inside MCP.
   - Always pass `"workDir"` set to the user's current active workspace directory so relative paths (`./sample/...`, `./article_coaching.md`) resolve inside the user's workspace (never write to `/tmp` unless explicitly requested). Windows paths (`C:\...`), WSL UNC paths (`\\wsl.localhost\...`), and Linux/WSL paths (`/mnt/c/...`) are normalized automatically.
2. **Host-AI (Agent-Side LLM) Execution in MCP Mode (`aiResult` / `aiSummaryResult` / `generatedDraft` — 0 Internal Gemini Tokens)**:
   - When invoked via MCP, `gdocumentor` does **not** call an internal Gemini API (`GEMINI_API_KEY` is not needed); instead, **YOU (the AI agent) generate the coaching review, summary, or draft using your own LLM**!
   - **1-Step Fast Path (Recommended for Local Workspace Files)**: Read the target file(s) (and any `-r` rulebooks or local images) with `read_file`, generate the structured `"aiResult"` JSON (`overallFeedback`, `learningSummary`, `existingSuggestionsNote`, `suggestions`, `elementSuggestions` where each `originalText` is an exact single-line substring from the target text), and pass `"aiResult"` directly in a **single** `gdocumentor_proofread` call!
   - **2-Step Universal Path (For Existing Google Docs `docId`, Remote URLs, PDFs/DOCX, or `--auto-deps`)**: Call `gdocumentor_proofread` first without `aiResult` (or with `"prepareOnly": true`) to receive `preparedContext` (`documentId`, `editableBlocks`, `tables`, `visualElements`, `coachingPrompt`), generate `"aiResult"` using your own LLM, and call `gdocumentor_proofread` a second time with `"docId": "<documentId>"`, `"skipDocInsert": true`, and `"aiResult": { ... }`!
   - Similarly, pass `"aiSummaryResult"` to `gdocumentor_summary` (after inspecting/preparing) and `"generatedDraft"` to `gdocumentor_draft`.
3. **Direct Google Docs, Rich Markdown & Image Editing (`gdocumentor_edit` & `gdocumentor_inspect` — 0 Gemini Tokens)**:
   - To create a Google Document with initial paragraphs, call `gdocumentor_edit` with `"createDoc": true`, `"title": "<Title>"`, and `"text": "Paragraph 1\nParagraph 2"`.
   - To insert rich Markdown (including tables and local images) between paragraphs, call `gdocumentor_edit` with `"workDir": "<active-workspace>"`, `"docId": "<DOC_ID>"`, `"inputs": ["./path/to/file.md"]`, `"beforeParagraph": 2` (or `"afterParagraph": 1` / `"before": "<phrase>"`), and `"markdown": true`.
   - To resize an existing image (`VISUAL_1`) in place, call `gdocumentor_edit` with `"docId": "<DOC_ID>"`, `"replaceImage": "VISUAL_1"` (or `"resizeImage": "VISUAL_1"`), and `"imageWidth": 300` (optionally with `"withImage": "./path/to/image.png"` if also replacing the image source).
3. **Partial Script Review in a Local Directory**:
   - If the user asks to proofread a specific function, block, or line range inside a local directory, first inspect the file to identify its exact line numbers (`start-end`) if not already specified.
   - Call `call_mcp_tool` (`gdocumentor_proofread`) with `"workDir": "<local-dir>"`, `"targets": ["<relative-file-path>:<start>-<end>"]`, and `"autoDeps": true` (plus any explicit `"contexts"` and `"rules"`).
4. **Partial Script Review in a Remote GitHub Repository**:
   - When the user specifies a GitHub repository URL and a partial script, call `call_mcp_tool` (`gdocumentor_proofread`) with `"repoUrl": "https://github.com/<owner>/<repo>"`, `"targets": ["<path/to/script>:<start>-<end>"]`, and `"autoDeps": true` (or pass the Blob URL `"https://github.com/<owner>/<repo>/blob/<branch>/<path/to/script>#L<start>-L<end>"` in `"targets"` with `"autoDeps": true`).
5. **Always Report the Context Ledger**:
   - In your response to the user, always include the clickable `documentUrl` (or `outputFilePath` in `noAuth` mode), the `learningSummary` (when proofreading/summarizing), and the list of `referenceContextFiles` that were loaded to verify cross-file consistency.
