package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

const defaultHost = "http://localhost:11434"
const defaultPrompt = "Translate the following text to %s. Return only the translation, nothing else.\n\n%s"

// OllamaTranslator translates text using an Ollama LLM model.
type OllamaTranslator struct {
	host   string
	model  string
	prompt string
	http   *http.Client
}

// OllamaOption configures an OllamaTranslator.
type OllamaOption func(*OllamaTranslator)

// WithModel sets the Ollama model to use for translation.
func WithModel(model string) OllamaOption {
	return func(o *OllamaTranslator) { o.model = model }
}

// WithHost sets the Ollama server URL (default: http://localhost:11434).
func WithHost(host string) OllamaOption {
	return func(o *OllamaTranslator) { o.host = host }
}

// WithPrompt sets a custom prompt template. Use %s placeholders for target language and text.
// Default: "Translate the following text to %s. Return only the translation, nothing else.\n\n%s"
func WithPrompt(prompt string) OllamaOption {
	return func(o *OllamaTranslator) { o.prompt = prompt }
}

func ollamaDefaultHost() string {
	if h := os.Getenv("OLLAMA_HOST"); h != "" {
		return normalizeHost(h)
	}
	return defaultHost
}

func normalizeHost(h string) string {
	if !strings.HasPrefix(h, "http://") && !strings.HasPrefix(h, "https://") {
		h = "http://" + h
	}
	if !strings.Contains(h[8:], ":") {
		h += ":11434"
	}
	return h
}

// NewOllama creates an OllamaTranslator. A model must be provided via WithModel.
func NewOllama(opts ...OllamaOption) *OllamaTranslator {
	o := &OllamaTranslator{
		host:   ollamaDefaultHost(),
		prompt: defaultPrompt,
		http:   http.DefaultClient,
	}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

var translationSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"translation": map[string]string{"type": "string"},
	},
	"required":             []string{"translation"},
	"additionalProperties": false,
}

type chatRequest struct {
	Model    string           `json:"model"`
	Messages []chatMessage    `json:"messages"`
	Format   map[string]any   `json:"format"`
	Stream   bool             `json:"stream"`
	Options  map[string]any   `json:"options,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
}

type translationResult struct {
	Translation string `json:"translation"`
}

// Translate sends text to Ollama for translation into toLang.
func (o *OllamaTranslator) Translate(ctx context.Context, text, toLang string) (string, error) {
	if o.model == "" {
		return "", fmt.Errorf("ollama: model not set")
	}

	prompt := fmt.Sprintf(o.prompt, toLang, text)

	req := chatRequest{
		Model: o.model,
		Messages: []chatMessage{
			{Role: "user", Content: prompt},
		},
		Format:  translationSchema,
		Stream:  false,
		Options: map[string]any{"temperature": 0},
	}

	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("ollama: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", o.host+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("ollama: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := o.http.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("ollama: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("ollama: server returned %d: %s", resp.StatusCode, string(b))
	}

	var chatResp chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return "", fmt.Errorf("ollama: decode response: %w", err)
	}

	var result translationResult
	if err := json.Unmarshal([]byte(chatResp.Message.Content), &result); err != nil {
		return "", fmt.Errorf("ollama: decode translation: %w", err)
	}

	return result.Translation, nil
}
