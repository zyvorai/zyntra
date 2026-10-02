// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-1.0

package ai

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Provider is an optional OpenAI-compatible chat model (OpenAI, Ollama,
// vLLM, or Fabric's AI gateway).
type Provider struct {
	BaseURL string
	APIKey  string
	Model   string
	Label   string
	HTTP    *http.Client
}

func NewProvider(baseURL, apiKey, model, label string, insecure bool) *Provider {
	if model == "" {
		model = DefaultModel
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // self-signed lab gateway
	}
	return &Provider{
		BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, Model: model, Label: label,
		HTTP: &http.Client{Timeout: 180 * time.Second, Transport: tr},
	}
}

// DefaultModel is the small local model documented for air-gapped sites:
// served by Fabric's AI gateway or Ollama on the same network. Zyntra never
// picks a cloud endpoint by itself; with no ZYNTRA_AI_BASE_URL it stays on
// the heuristic answers.
const DefaultModel = "qwen2.5:7b-instruct"

const systemPrompt = `You are Zyntra, a decision-intelligence assistant for infrastructure operators.
Rewrite the DRAFT answer so it reads naturally for an operator. Rules:
- Use only facts, numbers and names present in the JSON SNAPSHOT or the DRAFT. Never invent metrics, KPIs, actions or values.
- Keep every number exactly as given.
- Never claim an action was executed. Actions only run after a human approves them.
- No secrets. Under 160 words. Plain prose, short sentences, no headings.`

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Rewrite asks the model to restate draft using only snapshot.
func (p *Provider) Rewrite(ctx context.Context, question, draft string, snapshot any) (string, error) {
	snap, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	if len(snap) > 24<<10 {
		snap = snap[:24<<10]
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	return p.Chat(ctx, systemPrompt, "QUESTION: "+question+"\n\nDRAFT:\n"+draft+"\n\nSNAPSHOT:\n"+string(snap), false)
}

// Chat sends one system and one user message. With jsonOut the model is
// asked for a JSON object and any code fence around it is removed.
func (p *Provider) Chat(ctx context.Context, system, user string, jsonOut bool) (string, error) {
	req := map[string]any{
		"model":       p.Model,
		"temperature": 0.2,
		"messages":    []chatMessage{{Role: "system", Content: system}, {Role: "user", Content: user}},
	}
	if jsonOut {
		req["response_format"] = map[string]string{"type": "json_object"}
	}
	body, _ := json.Marshal(req)
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	hr.Header.Set("Content-Type", "application/json")
	if p.APIKey != "" {
		hr.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	resp, err := p.HTTP.Do(hr)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("llm: HTTP %d: %.200s", resp.StatusCode, b)
	}
	var out struct {
		Choices []struct {
			Message chatMessage `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return "", fmt.Errorf("llm: decode: %w", err)
	}
	if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("llm: empty response")
	}
	text := stripThinking(out.Choices[0].Message.Content)
	if jsonOut {
		text = stripFence(text)
	}
	return text, nil
}

// stripFence removes a ```json ... ``` fence and anything outside the
// outermost braces.
func stripFence(s string) string {
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j < i {
		return s
	}
	return s[i : j+1]
}

// stripThinking drops <think>...</think> blocks some open models emit.
func stripThinking(s string) string {
	for {
		i := strings.Index(s, "<think>")
		if i < 0 {
			break
		}
		j := strings.Index(s[i:], "</think>")
		if j < 0 {
			s = s[:i]
			break
		}
		s = s[:i] + s[i+j+len("</think>"):]
	}
	return strings.TrimSpace(s)
}
