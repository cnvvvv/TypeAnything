package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// LLMClient calls an OpenAI Chat-Completions-compatible endpoint. Both
// the C++ processor (WinHTTP) and this Go version target the same API:
//   POST https://<host><path>
//   Authorization: Bearer <api_key>
//   {"model": "...", "temperature": ..., "messages": [...]}
//
// Two response shapes are handled by ExtractContent:
//   * OpenAI / DeepSeek / Moonshot / 智谱 / Ollama-v1:
//       "choices":[{"message":{"content":"..."}}]
//   * Anthropic /v1/messages:
//       "content":[{"type":"text","text":"..."}]
//
// The Go version uses encoding/json + interface{} walk rather than the
// C++ hand-rolled byte scanner, but the parse rules are identical so
// responses from the same provider parse the same way.
type LLMClient struct {
	HTTPClient *http.Client
}

// NewLLMClient returns a client with a 30s timeout (matching the C++
// WinHttpSetTimeouts of 15s × 4 stages ≈ generous ceiling). Override
// HTTPClient on the returned struct if you need different transport.
func NewLLMClient() *LLMClient {
	return &LLMClient{
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// ChatRequest is the payload sent to the endpoint.
type ChatRequest struct {
	Model       string        `json:"model"`
	Temperature float64       `json:"temperature"`
	Messages    []ChatMessage `json:"messages"`
}

// ChatMessage is one message in the OpenAI messages array.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatResult holds the parsed response. HTTPStatus is the raw HTTP code
// (200/401/403/...); Body is the raw response body for diagnostics and
// logging; Content is the extracted assistant text (empty on failure).
type ChatResult struct {
	HTTPStatus int
	Body       string
	Content    string
	Err        error // transport-level error; nil on successful HTTP round-trip
}

// Call POSTs the request and returns the parsed result. The caller is
// responsible for distinguishing "no content" (network/parse failure)
// from "401/403" (auth) by inspecting HTTPStatus.
//
// The URL is built as https://<host><path> — same as the C++ version,
// which hardcodes INTERNET_DEFAULT_HTTPS_PORT. If you need HTTP or a
// non-default port, extend Provider to carry a full URL.
func (c *LLMClient) Call(ctx context.Context, provider Provider, req ChatRequest) ChatResult {
	body, err := json.Marshal(req)
	if err != nil {
		return ChatResult{Err: fmt.Errorf("llm: marshal: %w", err)}
	}

	url := "https://" + provider.Host + provider.Path
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return ChatResult{Err: fmt.Errorf("llm: new request: %w", err)}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+provider.APIKey)
	httpReq.Header.Set("User-Agent", "TypeAnything/2.0-go")

	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return ChatResult{Err: fmt.Errorf("llm: post: %w", err)}
	}
	defer resp.Body.Close()

	raw, readErr := io.ReadAll(resp.Body)
	result := ChatResult{
		HTTPStatus: resp.StatusCode,
		Body:       string(raw),
	}
	if readErr != nil {
		result.Err = fmt.Errorf("llm: read body: %w", readErr)
		return result
	}

	// Extract content from whichever shape the API used. Empty content
	// is not an error here — the caller (pipeline) decides what to do
	// based on HTTPStatus + Content.
	result.Content = ExtractContent(result.Body)
	return result
}

// ExtractContent pulls the assistant's text out of an OpenAI- or
// Anthropic-style JSON response body. Returns "" on any failure
// (matches the C++ ExtractContent which returns "" when the scan fails).
//
// OpenAI shape: {"choices":[{"message":{"content":"..."}}]}
// Anthropic shape: {"content":[{"type":"text","text":"..."}]}
//
// We try Anthropic first when the top-level "content" field is an array,
// otherwise fall back to the OpenAI choices path. This is more robust
// than the C++ byte scan because we let encoding/json disambiguate the
// shape, but the output is byte-compatible with the C++ extraction for
// any well-formed response.
func ExtractContent(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &top); err != nil {
		return ""
	}

	// Anthropic: top-level "content" is an array of {type,text}.
	if raw, ok := top["content"]; ok && len(raw) > 0 && raw[0] == '[' {
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(raw, &blocks) == nil {
			for _, b := range blocks {
				if b.Type == "text" && b.Text != "" {
					return b.Text
				}
			}
		}
		// Fall through to OpenAI path on parse failure.
	}

	// OpenAI: choices[0].message.content
	if raw, ok := top["choices"]; ok {
		var choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(raw, &choices) == nil && len(choices) > 0 {
			return choices[0].Message.Content
		}
	}
	return ""
}

// Provider mirrors config.Provider but is duplicated here to keep the
// translate package free of an import cycle (config imports nothing from
// translate; translate shouldn't reach back up). The pipeline constructs
// this from a config.Provider snapshot at call time.
type Provider struct {
	APIKey      string
	Model       string
	Host        string
	Path        string
	Temperature float64
}

// FallbackSystemPrompt is the generic translator prompt used when no
// category-specific prompt is available (legacy lang.txt without the
// X: prefix, or prompts.txt missing). Byte-identical to the C++ fallback
// including the byte-escaped "你好" so we don't introduce a cp936 hazard.
//
// The C++ embeds this as a UTF-8 byte string literal to avoid MSVC's
// cp936 source corruption (AGENTS.md "No Chinese string literals" rule).
// In Go that rule doesn't apply (Go source is always UTF-8), but we keep
// the same byte sequence for byte-for-byte prompt equivalence with the
// C++ build, so eval results stay comparable across versions.
const FallbackSystemPrompt = "You are a professional translator. Translate the user's Chinese into " +
	"fluent, idiomatic {LANG}. Rules:\n" +
	"1. Preserve tone (casual, formal, technical, polite, etc.).\n" +
	"2. Adapt cultural references and idioms to the target language; do not " +
	"literally word-translate.\n" +
	"3. Match register: '你好' in casual chat = 'Hi' not 'Greetings'.\n" +
	"4. Keep proper nouns / English terms / numbers / URLs unchanged.\n" +
	"5. Output ONLY the translation. No quotes, no markdown, no notes, no " +
	"prefix like 'Translation:'. Just the target text, nothing else."

// BuildSystemPrompt assembles the system prompt for a translation:
// if category-specific template is provided, substitute {LANG} → lang;
// otherwise use the fallback. This is the exact branching the C++
// DispatchTranslate does.
func BuildSystemPrompt(category byte, lang, categoryTemplate string) string {
	if category != 0 && categoryTemplate != "" {
		return strings.ReplaceAll(categoryTemplate, "{LANG}", lang)
	}
	return strings.ReplaceAll(FallbackSystemPrompt, "{LANG}", lang)
}
