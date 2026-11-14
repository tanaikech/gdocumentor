package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// jsonRPCRequest represents an incoming JSON-RPC 2.0 request or notification over MCP stdio.
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// jsonRPCResponse represents an outgoing JSON-RPC 2.0 response over MCP stdio.
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// mcpServerOverrides allows unit tests to inject mock API base URLs into MCP tool invocations.
type mcpServerOverrides struct {
	DocsBaseURL   string
	DriveBaseURL  string
	GeminiBaseURL string
	WorkDir       string
	AccessToken   string
	APIKey        string
	NoAuth        bool
}

// RunMCPServer runs the Model Context Protocol (MCP) JSON-RPC 2.0 server over stdin/stdout.
func RunMCPServer(ctx context.Context, stdin io.Reader, stdout io.Writer, overrides *mcpServerOverrides) error {
	if stdin == nil || stdout == nil {
		return nil
	}
	reader := bufio.NewReader(stdin)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		msgBytes, err := readNextJSONRPCMessage(reader)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if len(bytes.TrimSpace(msgBytes)) == 0 {
			continue
		}

		var req jsonRPCRequest
		if err := json.Unmarshal(msgBytes, &req); err != nil {
			writeJSONRPCResponse(stdout, jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      json.RawMessage("null"),
				Error: &jsonRPCError{
					Code:    -32700,
					Message: "Parse error: " + err.Error(),
				},
			})
			continue
		}

		// JSON-RPC 2.0 Notifications (no ID field) do not receive a response
		if len(req.ID) == 0 {
			continue
		}

		resp := handleMCPRequest(ctx, req, overrides)
		writeJSONRPCResponse(stdout, resp)
	}
}

func readNextJSONRPCMessage(r *bufio.Reader) ([]byte, error) {
	for {
		line, err := r.ReadString('\n')
		if err != nil && len(line) == 0 {
			return nil, err
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if err != nil {
				return nil, err
			}
			continue
		}

		// Support LSP-style Content-Length framing if used by an MCP client
		if strings.HasPrefix(strings.ToLower(trimmed), "content-length:") {
			valStr := strings.TrimSpace(trimmed[len("content-length:"):])
			contentLen, convErr := strconv.Atoi(valStr)
			if convErr != nil || contentLen < 0 {
				return nil, fmt.Errorf("invalid Content-Length header: %q", trimmed)
			}
			// Consume remaining headers until empty line
			for {
				hline, hErr := r.ReadString('\n')
				if hErr != nil {
					return nil, hErr
				}
				if strings.TrimSpace(hline) == "" {
					break
				}
			}
			buf := make([]byte, contentLen)
			if _, readErr := io.ReadFull(r, buf); readErr != nil {
				return nil, readErr
			}
			return buf, nil
		}

		return []byte(trimmed), nil
	}
}

func writeJSONRPCResponse(w io.Writer, resp jsonRPCResponse) {
	if resp.JSONRPC == "" {
		resp.JSONRPC = "2.0"
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return
	}
	_, _ = w.Write(append(b, '\n'))
}

func handleMCPRequest(ctx context.Context, req jsonRPCRequest, overrides *mcpServerOverrides) jsonRPCResponse {
	switch req.Method {
	case "initialize":
		protoVer := "2024-11-05"
		var initParams struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if len(req.Params) > 0 && json.Unmarshal(req.Params, &initParams) == nil && strings.TrimSpace(initParams.ProtocolVersion) != "" {
			protoVer = strings.TrimSpace(initParams.ProtocolVersion)
		}
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": protoVer,
				"capabilities": map[string]any{
					"tools": map[string]any{
						"listChanged": false,
					},
					"prompts": map[string]any{
						"listChanged": false,
					},
					"resources": map[string]any{
						"listChanged": false,
					},
				},
				"serverInfo": map[string]any{
					"name":    "gdocumentor",
					"version": Version,
				},
				"instructions": "Always call these gdocumentor MCP tools (gdocumentor_proofread, gdocumentor_summary, gdocumentor_inspect, gdocumentor_draft, gdocumentor_edit, gdocumentor_manage) via call_mcp_tool by default. Do NOT run the gdocumentor CLI binary via shell commands UNLESS the user's prompt explicitly instructs you to use gdocumentor as a CLI tool rather than as an MCP server (in which case the CLI tool uses GEMINI_API_KEY). Always pass workDir (set to the user's current workspace directory) so relative paths in inputs, targets, rules, outputPath, and Markdown images resolve inside the user's workspace. IMPORTANT — HOST-AI (AGENT-SIDE LLM) EXECUTION IN MCP MODE: When invoked as an MCP server, gdocumentor does NOT call an internal Gemini API (GEMINI_API_KEY is NOT required); instead, YOU (the AI agent) generate the proofreading suggestions, learning summary, or draft using your own LLM! For gdocumentor_proofread, you can use either: (A) 1-Step Fast Path (for local files): read the target file(s) (and any rules/images) with read_file, generate the aiResult JSON ({ overallFeedback, learningSummary: { revisionsDigest, weakPointsAndHabits, actionableTips }, existingSuggestionsNote, suggestions: [{ originalText, suggestedText, category, reason, learningPoint }], elementSuggestions: [{ targetId, category, suggestion, reason, learningPoint }] }) where each originalText is an exact single-line substring from the target text, and pass aiResult directly in a single gdocumentor_proofread call; OR (B) 2-Step Universal Path (for existing Google Docs docId, remote URLs, PDFs/DOCX, or --auto-deps): first call gdocumentor_proofread without aiResult (or with prepareOnly: true) to receive preparedContext (documentId, editableBlocks, tables, visualElements, coachingPrompt, expectedJsonSchema), generate aiResult using your own LLM, and call gdocumentor_proofread a second time with docId, skipDocInsert: true, and aiResult! Similarly, pass aiSummaryResult to gdocumentor_summary and generatedDraft to gdocumentor_draft.",
			},
		}

	case "ping":
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]any{},
		}

	case "tools/list":
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"tools": mcpToolDefinitions(),
			},
		}

	case "tools/call":
		return handleMCPToolCall(ctx, req, overrides)

	case "resources/list":
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"resources": []any{},
			},
		}

	case "resources/templates/list":
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"resourceTemplates": []any{},
			},
		}

	case "prompts/list":
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"prompts": mcpPromptDefinitions(),
			},
		}

	default:
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &jsonRPCError{
				Code:    -32601,
				Message: fmt.Sprintf("Method not found: %s", req.Method),
			},
		}
	}
}

func handleMCPToolCall(ctx context.Context, req jsonRPCRequest, overrides *mcpServerOverrides) jsonRPCResponse {
	var callParams struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &callParams); err != nil {
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &jsonRPCError{
				Code:    -32602,
				Message: "Invalid params for tools/call: " + err.Error(),
			},
		}
	}

	opts := &Options{
		Command:         "proofread",
		AutoDeps:        true,
		Mode:            "suggest",
		IncludeResolved: true,
		Lang:            "en",
		JSONOutput:      true,
	}
	if envNoAuth := strings.TrimSpace(os.Getenv("DOCUMENTOR_NO_AUTH")); envNoAuth != "" {
		opts.NoAuth = parseBoolStr(envNoAuth, false)
	}
	if overrides != nil && overrides.NoAuth {
		opts.NoAuth = true
	}
	if len(callParams.Arguments) > 0 && string(callParams.Arguments) != "null" {
		if err := json.Unmarshal(callParams.Arguments, opts); err != nil {
			return jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error: &jsonRPCError{
					Code:    -32602,
					Message: "Invalid tool arguments: " + err.Error(),
				},
			}
		}
		applyJSONPresenceFlags(callParams.Arguments, opts)
	}
	if strings.TrimSpace(opts.SummaryTabName) != "" {
		opts.SummaryTab = true
	}

	toolName := strings.TrimSpace(callParams.Name)
	switch toolName {
	case "gdocumentor_proofread", "proofread":
		opts.Command = "proofread"
	case "gdocumentor_summary", "summary":
		opts.Command = "summary"
	case "gdocumentor_inspect", "inspect":
		opts.Command = "inspect"
	case "gdocumentor_draft", "draft":
		opts.Command = "draft"
	case "gdocumentor_edit", "edit":
		opts.Command = "edit"
	case "gdocumentor_manage", "manage":
		opts.Command = "manage"
	default:
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &jsonRPCError{
				Code:    -32602,
				Message: fmt.Sprintf("Unknown tool %q", toolName),
			},
		}
	}

	opts.JSONOutput = true
	opts.FromMCP = true
	if envInternal := strings.TrimSpace(os.Getenv("DOCUMENTOR_MCP_USE_INTERNAL_GEMINI")); envInternal != "" {
		if parseBoolStr(envInternal, false) {
			opts.UseInternalGemini = true
		}
	}
	if overrides != nil {
		if overrides.DocsBaseURL != "" {
			opts.DocsBaseURL = overrides.DocsBaseURL
		}
		if overrides.DriveBaseURL != "" {
			opts.DriveBaseURL = overrides.DriveBaseURL
		}
		if overrides.GeminiBaseURL != "" {
			opts.GeminiBaseURL = overrides.GeminiBaseURL
			if !opts.PrepareOnly && !opts.UseAgentAI && opts.AIResult == nil && opts.AISummaryResult == nil && strings.TrimSpace(opts.GeneratedDraft) == "" {
				opts.UseInternalGemini = true
			}
		}
		if overrides.WorkDir != "" && opts.WorkDir == "" {
			opts.WorkDir = overrides.WorkDir
		}
		if overrides.AccessToken != "" && opts.AccessToken == "" {
			opts.AccessToken = overrides.AccessToken
		}
		if overrides.APIKey != "" && opts.APIKey == "" {
			opts.APIKey = overrides.APIKey
		}
	}
	if !opts.UseInternalGemini {
		opts.UseAgentAI = true
	}

	out, err := ExecuteOptions(ctx, opts, nil)
	if err != nil {
		errObj := map[string]any{
			"success": false,
			"command": opts.Command,
			"error":   err.Error(),
		}
		errJSON, _ := json.MarshalIndent(errObj, "", "  ")
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"content": []map[string]any{
					{
						"type": "text",
						"text": string(errJSON),
					},
				},
				"isError": true,
			},
		}
	}

	outJSON, _ := json.MarshalIndent(out, "", "  ")
	return jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]any{
			"content": []map[string]any{
				{
					"type": "text",
					"text": string(outJSON),
				},
			},
			"structuredContent": out,
			"isError":           false,
		},
	}
}

func mcpToolDefinitions() []map[string]any {
	return []map[string]any{
		{
			"name":        "gdocumentor_proofread",
			"description": "Proofread and review local manuscripts (.md with automatic Base64 local image embedding, .txt, .pdf, .docx converted to native Pageless Google Docs), source code / scripts (.go, .js, .ts, .py, .gs, .sh, etc. inserted into Pageless Google Docs), partial line/character ranges (targets, range), remote GitHub repositories/Blob URLs (repoUrl), Web article URLs, or an existing Google Document (docId). HOST-AI EXECUTION (0 INTERNAL GEMINI TOKENS): In MCP mode, gdocumentor uses YOUR (the client AI agent's) LLM instead of calling an internal Gemini API! You can either: (1) 1-Step Fast Path (for local files): read the target file(s)/rules/images with read_file, generate the structured review in aiResult, and pass aiResult directly in a single gdocumentor_proofread call; OR (2) 2-Step Universal Path (for existing Google Docs docId, remote URLs, PDFs/DOCX, or --auto-deps): call gdocumentor_proofread first without aiResult (or with prepareOnly: true) to get preparedContext (documentId, editableBlocks, tables, visualElements, coachingPrompt), generate aiResult with your own LLM, and call gdocumentor_proofread again with docId, skipDocInsert: true, and aiResult!",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"workDir": map[string]any{
						"type":        "string",
						"description": "Base working directory for resolving relative paths in inputs, targets, contexts, rules, and outputPath (set this to the user's active workspace directory).",
					},
					"inputs": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Target file path(s), directory path(s), or Web/GitHub URLs to proofread.",
					},
					"targets": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Target file(s) or URLs with specific line or character range, e.g. 'internal/cli/app.go:15-80', 'article.md@100:450', or 'https://github.com/owner/repo/blob/main/src/code.js#L15-L60'.",
					},
					"range": map[string]any{
						"type":        "string",
						"description": "Standalone line/paragraph range ('2-5'), UTF-16 offset range ('@100:600'), or section/heading phrase ('Introduction').",
					},
					"repoUrl": map[string]any{
						"type":        "string",
						"description": "GitHub repository URL to shallow-clone into an ephemeral workspace for resolving targets and --auto-deps.",
					},
					"contexts": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Read-only reference context file(s), directories, or URLs for cross-file consistency verification.",
					},
					"autoDeps": map[string]any{
						"type":        "boolean",
						"description": "Automatically resolve local imports/includes and adjacent Markdown chapters as read-only context (default: true).",
					},
					"rules": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Local RAG rulebook file(s), directory, or URL (.md, .txt, .json, .yaml, .docx, .pdf).",
					},
					"rulesDir": map[string]any{
						"type":        "string",
						"description": "Directory containing local RAG rulebook files.",
					},
					"driveRag": map[string]any{
						"type":        "string",
						"description": "Google Drive Folder or File ID/URL containing organization RAG rulebooks.",
					},
					"persona": map[string]any{
						"type":        "string",
						"description": "Custom AI coach persona / positioning (e.g. 'あなたは大学で日本語を教えている日本人です。'). Can also be defined inside RAG rulebooks under '# AIの位置づけ' or '# AI Persona'.",
					},
					"docId": map[string]any{
						"type":        "string",
						"description": "Existing Google Document ID or URL for in-place proofreading (if omitted with local files, creates a new Pageless Google Doc).",
					},
					"title": map[string]any{
						"type":        "string",
						"description": "Custom title when creating a new Google Document.",
					},
					"parentFolder": map[string]any{
						"type":        "string",
						"description": "Google Drive Folder ID or URL where the new Google Document should be created.",
					},
					"tabId": map[string]any{
						"type":        "string",
						"description": "Target tab ID within a multi-tab Google Document (e.g. 't.0').",
					},
					"summaryTab": map[string]any{
						"type":        "boolean",
						"description": "Set to true to create or update a dedicated Coaching Summary Tab ('🎓 DocuMentor AI Summary') in the Google Document so the user can review the summary inside Google Docs at any time.",
					},
					"summaryTabName": map[string]any{
						"type":        "string",
						"description": "Optional custom tab/tag title for the Google Docs Coaching Summary Tab (automatically enables summaryTab).",
					},
					"mode": map[string]any{
						"type":        "string",
						"enum":        []string{"suggest", "hint"},
						"description": "'suggest' (WriteMode.SUGGEST + educational reply) or 'hint' (Socratic Hint-Only coaching comments).",
					},
					"includeResolved": map[string]any{
						"type":        "boolean",
						"description": "Include resolved/accepted/rejected review threads as coaching context (default: true).",
					},
					"model": map[string]any{
						"type":        "string",
						"description": "Optional model label (defaults to 'agent-ai' in MCP Host-AI mode; when useInternalGemini is true, accepts 'gemini-3.8-flash', 'gemini-4-hydrogen', 'gemini-4-carbon', 'gemini-4-argon', or any Gemini model ID).",
					},
					"useInternalGemini": map[string]any{
						"type":        "boolean",
						"description": "Set to true to force gdocumentor to call the internal Gemini API (using GEMINI_API_KEY) instead of Host-AI mode.",
					},
					"geminiBaseUrl": map[string]any{
						"type":        "string",
						"description": "Optional custom Gemini API base URL or reverse proxy URL when useInternalGemini is true.",
					},
					"lang": map[string]any{
						"type":        "string",
						"enum":        []string{"en", "ja"},
						"description": "Coaching feedback and comment language: 'en' or 'ja'.",
					},
					"prompt": map[string]any{
						"type":        "string",
						"description": "Custom focus instructions for proofreading.",
					},
					"stdinContent": map[string]any{
						"type":        "string",
						"description": "Optional raw text content to proofread directly without a file path.",
					},
					"noAuth": map[string]any{
						"type":        "boolean",
						"description": "Set to true to run WITHOUT a Google OAuth2 Access Token (outputs the proofreading/hints report to a local file at outputFilePath).",
					},
					"saveLocal": map[string]any{
						"type":        "boolean",
						"description": "Set to true when using a Google OAuth2 Access Token to ALSO save a local Markdown report (<stem>_proofread.md / <stem>_hint.md) alongside the Google Document.",
					},
					"outputPath": map[string]any{
						"type":        "string",
						"description": "Local file path to write the proofreading report or Socratic coaching hints (e.g. 'review_report.md'). Works in BOTH noAuth:true mode AND Access-Token mode.",
					},
					"prepareOnly": map[string]any{
						"type":        "boolean",
						"description": "Set to true in Phase 1 to resolve files/dependencies/RAG rules, create/fetch the Google Doc, and return preparedContext (editableBlocks, tables, visualElements, coachingPrompt) for you to analyze with your own LLM.",
					},
					"skipDocInsert": map[string]any{
						"type":        "boolean",
						"description": "Set to true in Phase 2 when passing docId from Phase 1 together with inputs/targets so gdocumentor does not re-insert text into the already-created Google Doc.",
					},
					"baseRevisionId": map[string]any{
						"type":        "string",
						"description": "Optional document revisionId returned in Phase 1 preparedContext.revisionId. When supplied (or resolved automatically via local snapshot), gdocumentor re-anchors suggestions across concurrent collaborator edits before applying.",
					},
					"aiResult": map[string]any{
						"type":        "object",
						"description": "Proofreading & coaching result generated by YOUR (the client AI agent's) LLM. When provided, gdocumentor skips internal Gemini API calls (0 tokens) and directly maps your suggestions to exact UTF-16 coordinates and applies them to Google Docs (or writes the local report).",
						"properties": map[string]any{
							"overallFeedback": map[string]any{
								"type":        "string",
								"description": "High-level encouraging coaching summary (in lang: 'ja' or 'en').",
							},
							"learningSummary": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"revisionsDigest":     map[string]any{"type": "string"},
									"weakPointsAndHabits": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
									"actionableTips":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
								},
								"required": []string{"revisionsDigest", "weakPointsAndHabits", "actionableTips"},
							},
							"existingSuggestionsNote": map[string]any{
								"type":        "string",
								"description": "Note on how existing review threads were respected.",
							},
							"suggestions": map[string]any{
								"type": "array",
								"items": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"originalText":  map[string]any{"type": "string", "description": "Exact single-line substring from the target text/editableBlocks to replace or annotate."},
										"suggestedText": map[string]any{"type": "string", "description": "Replacement text (keep in same language/code syntax as originalText)."},
										"category":      map[string]any{"type": "string"},
										"reason":        map[string]any{"type": "string"},
										"learningPoint": map[string]any{"type": "string"},
									},
									"required": []string{"originalText", "suggestedText", "category", "reason", "learningPoint"},
								},
							},
							"elementSuggestions": map[string]any{
								"type": "array",
								"items": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"targetId":      map[string]any{"type": "string", "description": "Visual or table identifier (e.g. 'VISUAL_1', 'TABLE_1')."},
										"category":      map[string]any{"type": "string"},
										"suggestion":    map[string]any{"type": "string"},
										"reason":        map[string]any{"type": "string"},
										"learningPoint": map[string]any{"type": "string"},
									},
									"required": []string{"targetId", "category", "suggestion", "reason", "learningPoint"},
								},
							},
						},
						"required": []string{"overallFeedback", "learningSummary", "suggestions"},
					},
				},
			},
		},
		{
			"name":        "gdocumentor_summary",
			"description": "Read an existing Google Document (including pending inline suggestions, open comments, resolved review history, tables, and embedded images) and generate ONLY the Writing Coach & Learning Summary, item-by-item review analysis, and Coach Impact & Decision Record without modifying the document. HOST-AI EXECUTION: Call first with docId to get preparedContext (existingSuggestions, existingComments, resolvedReviews, coachingPrompt), synthesize aiSummaryResult using your own LLM, and call again with docId and aiSummaryResult (plus summaryTab/outputPath/saveLocal if requested).",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"workDir": map[string]any{
						"type":        "string",
						"description": "Base working directory for resolving relative paths in rules and outputPath (set this to the user's active workspace directory).",
					},
					"docId": map[string]any{
						"type":        "string",
						"description": "Existing Google Document ID or full URL.",
					},
					"tabId": map[string]any{
						"type":        "string",
						"description": "Target tab ID within a multi-tab Google Document.",
					},
					"summaryTab": map[string]any{
						"type":        "boolean",
						"description": "Set to true to create or update a dedicated Coaching Summary Tab ('🎓 DocuMentor AI Summary') in the Google Document.",
					},
					"summaryTabName": map[string]any{
						"type":        "string",
						"description": "Optional custom tab/tag title for the Google Docs Coaching Summary Tab (automatically enables summaryTab).",
					},
					"range": map[string]any{
						"type":        "string",
						"description": "Optional scope filter: paragraph range ('2-5'), UTF-16 offset range ('@100:600'), or heading/phrase text.",
					},
					"includeResolved": map[string]any{
						"type":        "boolean",
						"description": "Include resolved/accepted/rejected review history in the learning summary (default: true).",
					},
					"rules": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Local RAG rulebook file(s) or directory.",
					},
					"driveRag": map[string]any{
						"type":        "string",
						"description": "Google Drive Folder or File ID/URL containing organization rulebooks.",
					},
					"persona": map[string]any{
						"type":        "string",
						"description": "Custom AI coach persona / positioning.",
					},
					"model": map[string]any{
						"type":        "string",
						"description": "Optional model label (defaults to 'agent-ai' in MCP Host-AI mode; when useInternalGemini is true, accepts 'gemini-3.8-flash', 'gemini-4-hydrogen', 'gemini-4-carbon', 'gemini-4-argon', or any Gemini model ID).",
					},
					"useInternalGemini": map[string]any{
						"type":        "boolean",
						"description": "Set to true to force gdocumentor to call the internal Gemini API (using GEMINI_API_KEY) instead of Host-AI mode.",
					},
					"geminiBaseUrl": map[string]any{
						"type":        "string",
						"description": "Optional custom Gemini API base URL or reverse proxy URL when useInternalGemini is true.",
					},
					"lang": map[string]any{
						"type":        "string",
						"enum":        []string{"en", "ja"},
						"description": "Summary output language ('en' or 'ja').",
					},
					"prompt": map[string]any{
						"type":        "string",
						"description": "Custom focus instructions for the summary.",
					},
					"saveLocal": map[string]any{
						"type":        "boolean",
						"description": "Set to true to also save the learning summary report to a local Markdown file (<docId>_summary.md).",
					},
					"outputPath": map[string]any{
						"type":        "string",
						"description": "Optional local file path to save the learning summary report (e.g. 'summary_report.md').",
					},
					"prepareOnly": map[string]any{
						"type":        "boolean",
						"description": "Set to true in Phase 1 to fetch review threads and return preparedContext for your LLM.",
					},
					"aiSummaryResult": map[string]any{
						"type":        "object",
						"description": "Learning Summary JSON generated by YOUR (the client AI agent's) LLM ({ overallFeedback, learningSummary: { revisionsDigest, weakPointsAndHabits, actionableTips }, analyzedItems: [{ id, sourceType, targetPreview, proposedChangeOrComment, category, reason, learningPoint }] }).",
					},
				},
				"required": []string{"docId"},
			},
		},
		{
			"name":        "gdocumentor_inspect",
			"description": "Inspect an existing Google Document to list 1-based paragraph/block numbers (#1, #2...), exact UTF-16 character ranges (@start:end), pending inline suggestions (🤖 AI vs 👤 Human provenance), open comment threads, and the Coach Impact & Decision Record. Consumes zero Gemini API tokens.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"docId": map[string]any{
						"type":        "string",
						"description": "Existing Google Document ID or full URL.",
					},
					"tabId": map[string]any{
						"type":        "string",
						"description": "Target tab ID within a multi-tab Google Document.",
					},
					"lang": map[string]any{
						"type":        "string",
						"enum":        []string{"en", "ja"},
						"description": "Output language ('en' or 'ja').",
					},
				},
				"required": []string{"docId"},
			},
		},
		{
			"name":        "gdocumentor_draft",
			"description": "Generate a structured initial plain-text draft from a topic/outline prompt (grounded in optional reference files, RAG rulebooks, and custom AI Persona) and insert it into a newly created (Pageless) or existing blank Google Document (or save it directly to a local file when noAuth:true, outputPath, or saveLocal:true is set). HOST-AI EXECUTION: Write the initial draft text using your own LLM and pass it in generatedDraft (0 internal Gemini tokens), or call first with prepareOnly: true to load RAG rules/context files.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"workDir": map[string]any{
						"type":        "string",
						"description": "Base working directory for resolving relative paths in contexts, rules, and outputPath (set this to the user's active workspace directory).",
					},
					"prompt": map[string]any{
						"type":        "string",
						"description": "Topic outline and instructions for generating the initial draft.",
					},
					"generatedDraft": map[string]any{
						"type":        "string",
						"description": "Initial draft text generated by YOUR (the client AI agent's) LLM to insert into the Google Document or save locally without calling the internal Gemini API.",
					},
					"prepareOnly": map[string]any{
						"type":        "boolean",
						"description": "Set to true to resolve RAG rulebooks and context files and return preparedContext before writing generatedDraft.",
					},
					"title": map[string]any{
						"type":        "string",
						"description": "Title for the newly created Google Document.",
					},
					"docId": map[string]any{
						"type":        "string",
						"description": "Optional existing blank Google Document ID or URL.",
					},
					"parentFolder": map[string]any{
						"type":        "string",
						"description": "Optional Google Drive Folder ID or URL for the new document.",
					},
					"tabId": map[string]any{
						"type":        "string",
						"description": "Optional target tab ID.",
					},
					"contexts": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Reference context file(s) or URLs.",
					},
					"rules": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "RAG rulebook file(s) or directory.",
					},
					"driveRag": map[string]any{
						"type":        "string",
						"description": "Google Drive Folder or File ID/URL containing organization rulebooks.",
					},
					"persona": map[string]any{
						"type":        "string",
						"description": "Custom AI coach persona / positioning.",
					},
					"model": map[string]any{
						"type":        "string",
						"description": "Optional model label (defaults to 'agent-ai' in MCP Host-AI mode; when useInternalGemini is true, accepts 'gemini-3.8-flash', 'gemini-4-hydrogen', 'gemini-4-carbon', 'gemini-4-argon', or any Gemini model ID).",
					},
					"useInternalGemini": map[string]any{
						"type":        "boolean",
						"description": "Set to true to force gdocumentor to call the internal Gemini API (using GEMINI_API_KEY) instead of Host-AI mode.",
					},
					"geminiBaseUrl": map[string]any{
						"type":        "string",
						"description": "Optional custom Gemini API base URL or reverse proxy URL when useInternalGemini is true.",
					},
					"lang": map[string]any{
						"type":        "string",
						"enum":        []string{"en", "ja"},
						"description": "Draft output language ('en' or 'ja').",
					},
					"noAuth": map[string]any{
						"type":        "boolean",
						"description": "Set to true to run WITHOUT a Google OAuth2 Access Token and save the generated draft directly to a local file.",
					},
					"saveLocal": map[string]any{
						"type":        "boolean",
						"description": "Set to true when using a Google OAuth2 Access Token to ALSO save the generated draft to a local file (gdocumentor_draft.md).",
					},
					"outputPath": map[string]any{
						"type":        "string",
						"description": "Local file path to save the generated draft (e.g. 'draft.md'). Works in BOTH noAuth:true mode AND Access-Token mode.",
					},
				},
			},
		},
		{
			"name":        "gdocumentor_edit",
			"description": "Directly create or delete a Google Document, insert/write text or rich Markdown (headings, lists, tables, images) at any location (beginning, end, exact UTF-16 offset @N, paragraph number, or before/after any matching phrase), replace text or ranges, delete arbitrary ranges/text, or insert/replace/resize/delete images (VISUAL_1, etc.) in a Google Document without consuming any Gemini API tokens (0 tokens).",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"workDir": map[string]any{
						"type":        "string",
						"description": "Base working directory for resolving relative file/image paths in inputs, insertImage, withImage, and Markdown image links (set this to the user's active workspace directory).",
					},
					"docId": map[string]any{
						"type":        "string",
						"description": "Target Google Document ID or full URL (optional when createDoc is true).",
					},
					"tabId": map[string]any{
						"type":        "string",
						"description": "Optional target tab ID within a multi-tab Google Document.",
					},
					"createDoc": map[string]any{
						"type":        "boolean",
						"description": "Set to true to create a new Google Document (optionally with title, parentFolder, and initial text/inputs).",
					},
					"deleteDoc": map[string]any{
						"type":        "boolean",
						"description": "Set to true to permanently delete the Google Document from Google Drive.",
					},
					"title": map[string]any{
						"type":        "string",
						"description": "Title when creating a new Google Document.",
					},
					"parentFolder": map[string]any{
						"type":        "string",
						"description": "Optional Google Drive Folder ID or URL when creating a new Google Document.",
					},
					"text": map[string]any{
						"type":        "string",
						"description": "Text to insert/write into the document (or replacement text).",
					},
					"inputs": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Optional local file path(s) whose content should be inserted or used to create/replace the document.",
					},
					"position": map[string]any{
						"type":        "string",
						"description": "Insertion position: 'start' ('prepend'), 'end' ('append'), '@150' (UTF-16 index), or '3' (paragraph number).",
					},
					"at": map[string]any{
						"type":        "string",
						"description": "Target location to insert text: '@150' (1-based UTF-16 index), '3' (1-based paragraph number), 'start', or 'end'.",
					},
					"before": map[string]any{
						"type":        "string",
						"description": "Insert text immediately before the matching text/heading in the document.",
					},
					"after": map[string]any{
						"type":        "string",
						"description": "Insert text immediately after the matching text/heading in the document.",
					},
					"beforeParagraph": map[string]any{
						"type":        "integer",
						"description": "Insert text before 1-based paragraph number.",
					},
					"afterParagraph": map[string]any{
						"type":        "integer",
						"description": "Insert text after 1-based paragraph number.",
					},
					"style": map[string]any{
						"type":        "string",
						"description": "Optional named paragraph style for inserted text ('HEADING_1'..'HEADING_6', 'TITLE', 'SUBTITLE', 'NORMAL_TEXT').",
					},
					"find": map[string]any{
						"type":        "string",
						"description": "Search text/phrase to replace in the document.",
					},
					"replace": map[string]any{
						"type":        "string",
						"description": "Replacement text for 'find' or 'replaceRange'.",
					},
					"replaceRange": map[string]any{
						"type":        "string",
						"description": "Range spec ('@100:250', '2-4', 'all', or phrase) to replace with 'replace' or 'text'.",
					},
					"replaceBody": map[string]any{
						"type":        "boolean",
						"description": "Set to true to replace the entire body content of the target Google Document tab.",
					},
					"deleteRange": map[string]any{
						"type":        "string",
						"description": "Range spec ('@100:250', '3', '2-5', or 'all') to delete from the document.",
					},
					"deleteText": map[string]any{
						"type":        "string",
						"description": "Specific text/phrase to delete from the document.",
					},
					"clearBody": map[string]any{
						"type":        "boolean",
						"description": "Set to true to clear all body text in the target Google Document tab.",
					},
					"markdown": map[string]any{
						"type":        "boolean",
						"description": "Set to true to convert Markdown syntax (headings, bold/italic/code/links, lists, tables, images) into native Google Docs elements at the target location.",
					},
					"insertImage": map[string]any{
						"type":        "string",
						"description": "Local image file path (.png/.jpg/.gif/.webp) or image URL to insert at the target location.",
					},
					"replaceImage": map[string]any{
						"type":        "string",
						"description": "Target image identifier ('VISUAL_1', '1', objectId, '@index', or 'all') to replace (with withImage) or resize in place (with imageWidth/imageHeight; withImage is optional when resizing).",
					},
					"resizeImage": map[string]any{
						"type":        "string",
						"description": "Target image identifier ('VISUAL_1', '1', objectId, '@index', or 'all') to resize in place using imageWidth and/or imageHeight (withImage is optional).",
					},
					"withImage": map[string]any{
						"type":        "string",
						"description": "Replacement local image file path or image URL for replaceImage (optional when resizing an existing image via imageWidth/imageHeight).",
					},
					"deleteImage": map[string]any{
						"type":        "string",
						"description": "Target image identifier ('VISUAL_1', '1', objectId, '@index', or 'all') to delete from the document.",
					},
					"imageWidth": map[string]any{
						"type":        "number",
						"description": "Optional image width in points (PT) for inserted, replaced, or resized images (e.g. 300 for 300pt).",
					},
					"imageHeight": map[string]any{
						"type":        "number",
						"description": "Optional image height in points (PT) for inserted, replaced, or resized images.",
					},
					"range": map[string]any{
						"type":        "string",
						"description": "Optional scope restriction ('@100:500', '2-5', etc.) for find/deleteText/before/after.",
					},
					"matchCase": map[string]any{
						"type":        "boolean",
						"description": "Case-sensitive matching for find/deleteText/before/after (default: true).",
					},
					"occurrence": map[string]any{
						"type":        "integer",
						"description": "1-based occurrence number to target (0 = all occurrences).",
					},
					"editSuggest": map[string]any{
						"type":        "boolean",
						"description": "Set to true to apply the edit as a Google Docs suggestion (WriteMode.SUGGEST) instead of direct edit (WriteMode.EDIT).",
					},
					"lang": map[string]any{
						"type":        "string",
						"enum":        []string{"en", "ja"},
						"description": "Output language ('en' or 'ja').",
					},
				},
			},
		},
		{
			"name":        "gdocumentor_manage",
			"description": "Batch-manage a Google Document: create a new blank Google Document (createDoc), accept all pending inline suggestions (acceptAll), reject all pending inline suggestions (rejectAll), delete all active comment threads (deleteComments), reset the Coach Impact & Decision Record (resetRecord), or permanently delete the Google Document from Google Drive (deleteDoc).",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"docId": map[string]any{
						"type":        "string",
						"description": "Google Document ID or full URL (optional when createDoc is true).",
					},
					"createDoc": map[string]any{
						"type":        "boolean",
						"description": "Create a new blank Google Document on Google Drive.",
					},
					"title": map[string]any{
						"type":        "string",
						"description": "Optional title when createDoc is true.",
					},
					"parentFolder": map[string]any{
						"type":        "string",
						"description": "Optional Google Drive parent folder ID/URL when createDoc is true.",
					},
					"tabId": map[string]any{
						"type":        "string",
						"description": "Optional target tab ID.",
					},
					"acceptAll": map[string]any{
						"type":        "boolean",
						"description": "Accept all pending inline suggestions in the document.",
					},
					"rejectAll": map[string]any{
						"type":        "boolean",
						"description": "Reject (clear) all pending inline suggestions in the document.",
					},
					"deleteComments": map[string]any{
						"type":        "boolean",
						"description": "Delete all active comment threads in the document.",
					},
					"resetRecord": map[string]any{
						"type":        "boolean",
						"description": "Reset the Coach Impact & Decision Record statistics for the document.",
					},
					"deleteDoc": map[string]any{
						"type":        "boolean",
						"description": "Permanently delete the Google Document from Google Drive (and clean up its local Decision Record).",
					},
					"lang": map[string]any{
						"type":        "string",
						"enum":        []string{"en", "ja"},
						"description": "Output language ('en' or 'ja').",
					},
				},
			},
		},
	}
}

func mcpPromptDefinitions() []map[string]any {
	return []map[string]any{
		{
			"name":        "proofread-manuscript",
			"description": "Proofread a local Markdown, PDF, DOCX, or Text manuscript into a Pageless Google Document with inline suggestions and coaching rationales (and optional local Markdown report).",
			"arguments": []map[string]any{
				{
					"name":        "path",
					"description": "Path to the local manuscript file",
					"required":    true,
				},
				{
					"name":        "lang",
					"description": "Coaching language ('en' or 'ja')",
					"required":    false,
				},
			},
		},
		{
			"name":        "proofread-code-range",
			"description": "Proofread a specific line range of a script or source file with automatic dependency resolution (--auto-deps).",
			"arguments": []map[string]any{
				{
					"name":        "target",
					"description": "File path and line range, e.g. 'internal/cli/app.go:15-80'",
					"required":    true,
				},
			},
		},
		{
			"name":        "proofread-local-no-auth",
			"description": "Proofread a local file or public URL WITHOUT a Google OAuth2 Access Token (noAuth: true, Gemini API only) and save the suggestions or Socratic hints to a local Markdown file.",
			"arguments": []map[string]any{
				{
					"name":        "path",
					"description": "Path to the local file or public URL",
					"required":    true,
				},
				{
					"name":        "outputPath",
					"description": "Local output Markdown file path (e.g. 'review_report.md')",
					"required":    false,
				},
			},
		},
	}
}
