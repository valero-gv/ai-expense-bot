// Package agent implements a bounded LLM/tool loop. Tools never accept an owner
// identity: ownership comes exclusively from the transaction's bound Session.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}
type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
	Strict      bool           `json:"strict"`
}
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}
type LLM interface {
	Complete(context.Context, []Message, []Tool) (Message, error)
}

type Client struct {
	baseURL, model, key string
	http                *http.Client
}

// NewClient accepts an OpenAI-compatible API base URL (usually ending in /v1).
// A supplied HTTP client can configure proxy/TLS; request cancellation also obeys
// the Engine's per-update deadline. No request, message, or API key is logged.
func NewClient(baseURL, model, key string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &Client{strings.TrimRight(baseURL, "/"), model, key, httpClient}
}

func (c *Client) Complete(ctx context.Context, messages []Message, tools []Tool) (Message, error) {
	endpoint := c.baseURL
	if !strings.HasSuffix(endpoint, "/chat/completions") {
		endpoint += "/chat/completions"
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return Message{}, fmt.Errorf("invalid LLM API base URL")
	}
	if strings.TrimSpace(c.model) == "" {
		return Message{}, fmt.Errorf("LLM model is empty")
	}
	body, err := json.Marshal(struct {
		Model             string    `json:"model"`
		Messages          []Message `json:"messages"`
		Tools             []Tool    `json:"tools"`
		Temperature       float64   `json:"temperature"`
		MaxTokens         int       `json:"max_tokens"`
		ParallelToolCalls bool      `json:"parallel_tool_calls"`
	}{c.model, messages, tools, 0, 1200, false})
	if err != nil {
		return Message{}, fmt.Errorf("encode LLM request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Message{}, fmt.Errorf("create LLM request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Message{}, fmt.Errorf("LLM request: %w", ctx.Err())
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return Message{}, fmt.Errorf("LLM request: %w", context.DeadlineExceeded)
		}
		if errors.Is(err, context.Canceled) {
			return Message{}, fmt.Errorf("LLM request: %w", context.Canceled)
		}
		// Do not include URL credentials, request text, or provider response bodies.
		return Message{}, fmt.Errorf("LLM transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Message{}, fmt.Errorf("LLM HTTP status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return Message{}, fmt.Errorf("read LLM response: %w", err)
	}
	if len(data) > 1<<20 {
		return Message{}, fmt.Errorf("LLM response exceeds limit")
	}
	var result struct {
		Choices []struct {
			Message      Message `json:"message"`
			FinishReason string  `json:"finish_reason"`
		} `json:"choices"`
	}
	if err = json.Unmarshal(data, &result); err != nil || len(result.Choices) != 1 {
		return Message{}, fmt.Errorf("invalid LLM response")
	}
	choice := result.Choices[0]
	if choice.FinishReason != "stop" && choice.FinishReason != "tool_calls" {
		if choice.FinishReason == "length" || choice.FinishReason == "content_filter" {
			return Message{}, fmt.Errorf("LLM response incomplete (%s)", choice.FinishReason)
		}
		return Message{}, fmt.Errorf("invalid LLM finish reason")
	}
	if choice.Message.Role != "assistant" || len(choice.Message.Content) > 16000 {
		return Message{}, fmt.Errorf("invalid LLM assistant message")
	}
	if choice.FinishReason == "tool_calls" && len(choice.Message.ToolCalls) == 0 {
		return Message{}, fmt.Errorf("LLM returned no requested tool calls")
	}
	return choice.Message, nil
}
