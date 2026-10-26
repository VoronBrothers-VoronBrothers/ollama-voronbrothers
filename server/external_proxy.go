package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// externalProxyMiddleware intercepts /api/chat requests where the model name
// matches a configured external provider and proxies to its OpenAI-compatible endpoint.
func externalProxyMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPost {
			c.Next()
			return
		}

		body, err := readRequestBody(c.Request)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			c.Abort()
			return
		}

		model, ok := extractModelField(body)
		if !ok {
			c.Next()
			return
		}

		extRef, isExternal := tryParseExternalModel(model)
		if !isExternal {
			c.Next()
			return
		}

		handleExternalChat(c, extRef, body)
		c.Abort()
	}
}

// openaiChatRequest is the minimal OpenAI chat/completions request structure.
type openaiChatRequest struct {
	Model       string          `json:"model"`
	Messages    []openaiMessage `json:"messages"`
	Stream      bool            `json:"stream"`
	Tools       []openaiTool    `json:"tools,omitempty"` // [voron] function calling
	MaxTokens   *int            `json:"max_tokens,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
}

// openaiTool mirrors OpenAI's tool definition (Ollama sends the same shape).
type openaiTool struct {
	Type     string          `json:"type"`
	Function json.RawMessage `json:"function"`
}

type openaiInCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openaiMessage struct {
	Role       string          `json:"role"`
	Content    string          `json:"content"`
	Name       string          `json:"name,omitempty"` // [voron] tool results (role=tool)
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolCalls  []openaiOutCall `json:"tool_calls,omitempty"` // [voron] assistant tool calls
}

type openaiOutCall struct {
	ID       string         `json:"id,omitempty"`
	Type     string         `json:"type"`
	Function map[string]any `json:"function"`
}

// ollamaChatResponse is the Ollama /api/chat streaming response format (one per line).
type ollamaChatResponse struct {
	Model           string            `json:"model"`
	CreatedAt       time.Time         `json:"created_at"`
	Message         ollamaRespMessage `json:"message"`
	Done            bool              `json:"done"`
	DoneReason      string            `json:"done_reason,omitempty"`
	TotalDuration   time.Duration     `json:"total_duration,omitempty"`
	PromptEvalCount int               `json:"prompt_eval_count,omitempty"`
	EvalCount       int               `json:"eval_count,omitempty"`
	EvalDuration    time.Duration     `json:"eval_duration,omitempty"`
}

type ollamaRespMessage struct {
	Role      string          `json:"role"`
	Content   string          `json:"content"`
	Thinking  string          `json:"thinking,omitempty"`   // [voron] рассуждения модели (reasoning)
	ToolCalls []ollamaOutCall `json:"tool_calls,omitempty"` // [voron] tool calling
}

type ollamaOutCall struct {
	ID       string         `json:"id,omitempty"`
	Function map[string]any `json:"function"`
}

// openaiSSEChunk is a single SSE data payload from OpenAI streaming.
type openaiSSEChunk struct {
	Choices []struct {
		Delta struct {
			Role             string         `json:"role"`
			Content          string         `json:"content"`
			ReasoningContent string         `json:"reasoning_content,omitempty"` // [voron] deepseek/qwen thinking
			ToolCalls        []openaiInCall `json:"tool_calls"`                  // [voron] tool calling
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage,omitempty"`
}

func handleExternalChat(c *gin.Context, extRef *externalModelRef, body []byte) {
	startTime := time.Now()
	modelDisplay := extRef.Provider.Name + "/" + extRef.ModelID

	// Build OpenAI request from Ollama body
	reqBody, err := buildOpenAIRequest(extRef, body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("external provider: %s", err.Error())})
		return
	}

	// Send to external API
	url := extRef.Provider.BaseURL + "/chat/completions"
	outReq, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("external provider: %s", err.Error())})
		return
	}
	outReq.Header.Set("Content-Type", "application/json")
	outReq.Header.Set("Authorization", "Bearer "+extRef.Provider.APIKey)

	resp, err := http.DefaultClient.Do(outReq)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("external provider unreachable: %s", err.Error())})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		c.JSON(http.StatusBadGateway, gin.H{
			"error": fmt.Sprintf("external provider returned %d: %s", resp.StatusCode, string(errBody)),
		})
		return
	}

	// Stream SSE → Ollama jsonl
	c.Writer.Header().Set("Content-Type", "application/x-ndjson")
	c.Status(http.StatusOK)

	flusher, canFlush := c.Writer.(http.Flusher)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	var (
		evalCount    int
		promptTokens int
		finishReason string
		toolAcc      map[int]*openaiInCall // [voron] накопленные tool_calls по индексу
	)
	toolAcc = make(map[int]*openaiInCall)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		// SSE format: "data: {...}" or "data: [DONE]"
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimPrefix(line, "data:")
		data = strings.TrimSpace(data)

		if data == "[DONE]" {
			break
		}

		var chunk openaiSSEChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			slog.Debug("external proxy: skipping unparseable chunk", "data", data, "error", err)
			continue
		}

		// Extract content delta (+ reasoning, if the provider sends it)
		var content, thinking string
		for _, choice := range chunk.Choices {
			if choice.Delta.ReasoningContent != "" {
				thinking += choice.Delta.ReasoningContent
			}
			if choice.Delta.Content != "" {
				content = choice.Delta.Content
			}
			for _, tc := range choice.Delta.ToolCalls { // [voron] tool calling
				frag, ok := toolAcc[tc.Index]
				if !ok {
					frag = &openaiInCall{}
					toolAcc[tc.Index] = frag
				}
				frag.ID += tc.ID
				frag.Function.Name += tc.Function.Name
				frag.Function.Arguments += tc.Function.Arguments
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				finishReason = *choice.FinishReason
			}
		}

		if chunk.Usage != nil {
			promptTokens = chunk.Usage.PromptTokens
		}

		if content == "" && thinking == "" && finishReason == "" {
			continue
		}

		// Emit Ollama-style response line
		evalCount++
		resp := ollamaChatResponse{
			Model:     modelDisplay,
			CreatedAt: time.Now(),
			Message:   ollamaRespMessage{Role: "assistant", Content: content, Thinking: thinking},
			Done:      finishReason != "",
			EvalCount: evalCount,
		}

		if resp.Done {
			resp.DoneReason = mapFinishReason(finishReason)
			resp.TotalDuration = time.Since(startTime).Round(time.Millisecond)
			resp.PromptEvalCount = promptTokens
			resp.EvalDuration = time.Since(startTime).Round(time.Millisecond)
		}

		// [voron] tool calls: отдельная строка перед done-чанком
		if resp.Done && len(toolAcc) > 0 {
			if tcLine, mErr := json.Marshal(ollamaChatResponse{
				Model:     modelDisplay,
				CreatedAt: time.Now(),
				Message:   ollamaRespMessage{Role: "assistant", ToolCalls: buildToolCallsOut(toolAcc)},
			}); mErr == nil {
				c.Writer.Write(tcLine)
				c.Writer.Write([]byte("\n"))
				if canFlush {
					flusher.Flush()
				}
			}
		}

		lineBytes, err := json.Marshal(resp)
		if err != nil {
			continue
		}
		c.Writer.Write(lineBytes)
		c.Writer.Write([]byte("\n"))
		if canFlush {
			flusher.Flush()
		}

		if resp.Done {
			break
		}
	}

	if !canFlush {
		slog.Debug("external proxy: writer does not support flush")
	}
}

// ollamaInMessage is the subset of api.Message we need for the conversion.
type ollamaInMessage struct {
	Role       string            `json:"role"`
	Content    string            `json:"content"`
	ToolName   string            `json:"tool_name,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
	ToolCalls  []ollamaInOutCall `json:"tool_calls,omitempty"`
}

type ollamaInOutCall struct {
	ID       string         `json:"id"`
	Function map[string]any `json:"function"`
}

// buildOpenAIRequest converts an Ollama /api/chat body into OpenAI chat/completions format.
func buildOpenAIRequest(extRef *externalModelRef, ollamaBody []byte) ([]byte, error) {
	var req struct {
		Model    string            `json:"model"`
		Messages []ollamaInMessage `json:"messages"`
		Tools    []openaiTool      `json:"tools,omitempty"` // [voron] function calling
		Stream   *bool             `json:"stream,omitempty"`
		Options  map[string]any    `json:"options,omitempty"`
	}
	if err := json.Unmarshal(ollamaBody, &req); err != nil {
		return nil, fmt.Errorf("parse request: %w", err)
	}

	openaiReq := openaiChatRequest{
		Model:    extRef.ModelID, // strip provider prefix, send "vendor/model"
		Messages: make([]openaiMessage, 0, len(req.Messages)),
		Stream:   true,
	}

	// [voron] tools passthrough (Ollama и OpenAI используют одинаковый формат)
	openaiReq.Tools = req.Tools

	for _, m := range req.Messages {
		out := openaiMessage{Role: m.Role, Content: m.Content}
		if m.Role == "tool" {
			out.ToolCallID = m.ToolCallID
			out.Name = m.ToolName
		}
		for _, tc := range m.ToolCalls {
			fn := make(map[string]any, len(tc.Function))
			for k, v := range tc.Function {
				fn[k] = v
			}
			// [voron] OpenAI требует arguments как JSON-строку в истории сообщений
			if args, ok := fn["arguments"]; ok && args != nil {
				if b, err := json.Marshal(args); err == nil {
					fn["arguments"] = string(b)
				} else {
					fn["arguments"] = ""
				}
			}
			out.ToolCalls = append(out.ToolCalls, openaiOutCall{
				ID:       tc.ID,
				Type:     "function",
				Function: fn,
			})
		}
		openaiReq.Messages = append(openaiReq.Messages, out)
	}

	// [voron] universal external-model system prompt: applied here so raw API
	// clients get it too. Clients that already sent it (the TUIs read it from
	// ShowResponse.System) are detected by prependExternalSystemPrompt and the
	// prompt is not duplicated.
	openaiReq.Messages = prependExternalSystemPrompt(openaiReq.Messages, externalSystemPrompt())

	// Pass through some options if present
	if req.Options != nil {
		if maxTokens, ok := req.Options["num_predict"].(float64); ok && maxTokens > 0 {
			tokens := int(maxTokens)
			openaiReq.MaxTokens = &tokens
		}
		if temp, ok := req.Options["temperature"].(float64); ok {
			openaiReq.Temperature = &temp
		}
	}

	return json.Marshal(openaiReq)
}

func mapFinishReason(reason string) string {
	switch reason {
	case "stop":
		return "stop"
	case "length":
		return "length"
	case "tool_calls", "function_call":
		return "stop" // tools not supported in MVP
	default:
		return "stop"
	}
}

// buildToolCallsOut converts accumulated OpenAI tool-call fragments into the
// Ollama api.ToolCall shape (arguments decoded to a map when possible).
func buildToolCallsOut(acc map[int]*openaiInCall) []ollamaOutCall {
	indices := make([]int, 0, len(acc))
	for i := range acc {
		indices = append(indices, i)
	}
	sort.Ints(indices)

	out := make([]ollamaOutCall, 0, len(indices))
	for _, i := range indices {
		frag := acc[i]
		argsAny := any(frag.Function.Arguments)
		if frag.Function.Arguments != "" && frag.Function.Arguments != "null" {
			var decoded any
			if err := json.Unmarshal([]byte(frag.Function.Arguments), &decoded); err == nil {
				argsAny = decoded
			}
		}
		out = append(out, ollamaOutCall{
			ID:       frag.ID,
			Function: map[string]any{"name": frag.Function.Name, "arguments": argsAny},
		})
	}
	return out
}
