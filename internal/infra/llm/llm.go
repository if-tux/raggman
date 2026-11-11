// Package llm talks to any OpenAI-compatible API: local Ollama's /v1 by
// default, or a cloud provider for Answers (ADR-0004).
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	http    *http.Client
	baseURL string
	apiKey  string
	// SkipReasoning asks the model not to think before answering. Set it only
	// for local Ollama: many cloud providers reject reasoning_effort.
	SkipReasoning bool
}

// New targets baseURL (ending in /v1); apiKey may be empty for local Ollama.
//
// There is no overall timeout, since a thinking model can stream for minutes;
// callers bound requests with their context. Only a server that never starts
// responding is cut off.
func New(baseURL, apiKey string) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// ponytail: generous because a cold local model loads before replying.
	transport.ResponseHeaderTimeout = 2 * time.Minute
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		http:    &http.Client{Transport: transport},
	}
}

// NewChat targets the chat model. thinking lets a local thinking model reason
// before it replies; it only applies without an apiKey, which means local
// Ollama, the only target known to accept the setting.
func NewChat(baseURL, apiKey string, thinking bool) *Client {
	c := New(baseURL, apiKey)
	c.SkipReasoning = !thinking && apiKey == ""
	return c
}

// Stream receives a streamed chat completion as it arrives.
type Stream struct {
	// Token gets each piece of generated text.
	Token func(string) error
	// Thinking, if set, is called once when the model starts reasoning
	// before it answers. Reasoning text itself is never passed on.
	Thinking func() error
}

// Embed returns one vector per input, in input order.
func (c *Client) Embed(ctx context.Context, model string, inputs []string) ([][]float32, error) {
	var resp struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
	}
	req := map[string]any{"model": model, "input": inputs}
	if err := c.post(ctx, "/embeddings", req, &resp); err != nil {
		return nil, err
	}
	if len(resp.Data) != len(inputs) {
		return nil, fmt.Errorf("embeddings: got %d vectors for %d inputs", len(resp.Data), len(inputs))
	}

	vectors := make([][]float32, len(inputs))
	for _, d := range resp.Data {
		if d.Index < 0 || d.Index >= len(inputs) {
			return nil, fmt.Errorf("embeddings: index %d out of range", d.Index)
		}
		vectors[d.Index] = d.Embedding
	}
	return vectors, nil
}

// Message is one chat turn.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatStream runs a streamed chat completion.
func (c *Client) ChatStream(ctx context.Context, model string, messages []Message, stream Stream) error {
	return c.chat(ctx, map[string]any{"model": model, "messages": messages}, stream)
}

// ChatJSON runs a chat completion whose reply must be a JSON object, and
// returns the whole reply. The prompt has to ask for JSON as well: providers
// reject the JSON mode otherwise. The reply is still streamed underneath, so
// a slow model is not cut off for staying silent while it generates.
func (c *Client) ChatJSON(ctx context.Context, model string, messages []Message) (string, error) {
	var reply strings.Builder
	body := map[string]any{
		"model":           model,
		"messages":        messages,
		"response_format": map[string]string{"type": "json_object"},
	}
	err := c.chat(ctx, body, Stream{Token: func(token string) error {
		reply.WriteString(token)
		return nil
	}})
	return reply.String(), err
}

func (c *Client) chat(ctx context.Context, body map[string]any, stream Stream) error {
	body["stream"] = true
	if c.SkipReasoning {
		body["reasoning_effort"] = "none"
	}
	resp, err := c.send(ctx, "/chat/completions", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	thinking := false
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			return nil
		}
		var chunk struct {
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
					// Ollama sends reasoning; vLLM and DeepSeek reasoning_content.
					Reasoning        string `json:"reasoning"`
					ReasoningContent string `json:"reasoning_content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return fmt.Errorf("chat stream: decoding chunk: %w", err)
		}
		if chunk.Error != nil {
			return fmt.Errorf("chat stream: %s", chunk.Error.Message)
		}
		for _, choice := range chunk.Choices {
			reasoning := choice.Delta.Reasoning + choice.Delta.ReasoningContent
			if reasoning != "" && !thinking && stream.Thinking != nil {
				thinking = true
				if err := stream.Thinking(); err != nil {
					return err
				}
			}
			if choice.Delta.Content == "" {
				continue
			}
			if err := stream.Token(choice.Delta.Content); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("chat stream: %w", err)
	}
	return errors.New("chat stream ended without [DONE]")
}

func (c *Client) post(ctx context.Context, path string, body, out any) error {
	resp, err := c.send(ctx, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("POST %s: decoding response: %w", path, err)
	}
	return nil
}

// send POSTs body as JSON and returns the response if it is 200 OK.
func (c *Client) send(ctx context.Context, path string, body any) (*http.Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("POST %s: %s: %s", path, resp.Status, bytes.TrimSpace(msg))
	}
	return resp, nil
}
