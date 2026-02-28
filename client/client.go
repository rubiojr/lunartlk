package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
)

// TranscriptLine represents a single line of transcribed text with timing.
type TranscriptLine struct {
	Text      string  `json:"text"`
	StartTime float64 `json:"start_time"`
	Duration  float64 `json:"duration"`
}

// TranscriptResponse holds the server's transcription result.
type TranscriptResponse struct {
	Text          string           `json:"text"`
	Lines         []TranscriptLine `json:"lines"`
	AudioDuration float64          `json:"audio_duration"`
	ProcessingMs  int64            `json:"processing_ms"`
	Model         string           `json:"model"`
	Lang          string           `json:"lang"`
	Engine        string           `json:"engine"`
	Arch          int              `json:"arch"`
}

// Client communicates with a lunartlk transcription server.
type Client struct {
	serverURL string
	token     string
	lang      string
	engine    string
	http      *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithToken sets the Bearer token for server authentication.
func WithToken(token string) Option {
	return func(c *Client) { c.token = token }
}

// WithLang sets the transcription language (e.g. "en", "es").
func WithLang(lang string) Option {
	return func(c *Client) { c.lang = lang }
}

// WithEngine sets the transcription engine (e.g. "moonshine", "parakeet").
func WithEngine(engine string) Option {
	return func(c *Client) { c.engine = engine }
}

// New creates a Client for the given server URL.
func New(serverURL string, opts ...Option) *Client {
	c := &Client{
		serverURL: strings.TrimRight(serverURL, "/"),
		http:      http.DefaultClient,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Transcribe sends encoded audio to the server and returns the transcript.
func (c *Client) Transcribe(audio []byte, filename string) (*TranscriptResponse, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("audio", filename)
	if err != nil {
		return nil, fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(audio); err != nil {
		return nil, fmt.Errorf("write audio: %w", err)
	}
	writer.Close()

	url := c.transcribeURL()
	req, err := http.NewRequest("POST", url, &body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, string(b))
	}

	var result TranscriptResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &result, nil
}

func (c *Client) transcribeURL() string {
	url := c.serverURL + "/transcribe"
	var params []string
	if c.lang != "" {
		params = append(params, "lang="+c.lang)
	}
	if c.engine != "" {
		params = append(params, "engine="+c.engine)
	}
	if len(params) > 0 {
		url += "?" + strings.Join(params, "&")
	}
	return url
}
