// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"encoding/json"
	"math"
)

// imageTokenAllowance is the conservative per-image token cost added to the
// input estimate: vision models tile images (OpenAI-style tiling is roughly
// 85 tokens per tile; a full image is at most ~1104 tokens). A fixed 256
// keeps the estimate conservative — an image request is never routed to an
// endpoint whose context budget is too small — without a real tokenizer.
const imageTokenAllowance = 256

// contextReserveFloor is the minimum safety margin added to the required
// context estimate, so a request is never routed to an endpoint whose max
// context equals the estimate exactly.
const contextReserveFloor = 4096

// contextReservePct is the fraction of the input byte length added on top of
// contextReserveFloor when the input is large.
const contextReservePct = 0.05

// Req is a classified request: enough of an OpenAI/Ollama body to establish
// eligibility. It never carries prompt or response content, only derived
// flags and token budgets.
type Req struct {
	Model             string
	Text              bool
	HasImages         bool
	ImageCount        int
	RequiresTools     bool
	RequiresStreaming bool
	// APIFamily is the request's wire protocol family ("openai", "ollama").
	// Empty (the normal case) means the request does not constrain the
	// candidate's declared family.
	APIFamily       string
	InputTokens     int
	MaxOutput       int
	RequiredContext int
}

// probe is the minimal decode target. It reads just the fields needed for
// classification without a full model-specific parse.
type probe struct {
	Model string `json:"model"`
	// StreamPtr distinguishes an explicit stream=false from an omitted stream:
	// an omitted stream means the request does not require streaming support.
	StreamPtr *bool `json:"stream"`
	// MaxTokensPtr distinguishes an explicit max_tokens=0 (a valid "no output"
	// request) from an omitted one.
	MaxTokensPtr *int   `json:"max_tokens"`
	MaxOutputPtr *int   `json:"max_completion_tokens"`
	Tools        any    `json:"tools"`
	Prompt       string `json:"prompt"`
	Options      *struct {
		NumPredict *int `json:"num_predict"`
	} `json:"options"`
	Messages []messageContent `json:"messages"`
}

// Classify derives request requirements from a raw request body. It parses
// just enough JSON to detect images, tools, streaming, and a conservative
// context budget — no tokenizer. The input-token estimate is chars/4 of the
// prompt plus message text, plus the tool-schema payload and a fixed
// per-image allowance, so a request is never routed to an undersized
// endpoint. A malformed or empty body yields the zero Req (no panic, no
// secret leakage — only derived flags and budgets, never prompt content).
func Classify(body []byte) Req {
	if len(body) == 0 {
		return Req{}
	}
	var p probe
	if err := json.Unmarshal(body, &p); err != nil {
		// A body that fails to parse (empty, whitespace, malformed, or a JSON
		// value that is not an object) carries no classification information:
		// the zero Req. The proxy still forwards it; eligibility gates on
		// declared requirements, and there are none.
		return Req{}
	}

	req := Req{
		Model: p.Model,
		// A text inference request is the default: any body that names a model,
		// a prompt, or messages is a text request.
		Text:              p.Model != "" || p.Prompt != "" || len(p.Messages) > 0,
		RequiresStreaming: p.StreamPtr != nil && *p.StreamPtr,
	}

	// Max output precedence: an explicit max_completion_tokens (OpenAI) wins;
	// then max_tokens (OpenAI); then Ollama options.num_predict. An explicit 0
	// on a higher-priority field is a valid "no output requested" value and
	// still wins over a lower-priority field.
	switch {
	case p.MaxOutputPtr != nil:
		req.MaxOutput = maxZero(*p.MaxOutputPtr)
	case p.MaxTokensPtr != nil:
		req.MaxOutput = maxZero(*p.MaxTokensPtr)
	case p.Options != nil && p.Options.NumPredict != nil:
		req.MaxOutput = maxZero(*p.Options.NumPredict)
	}

	// Tools present (non-empty array) means the request requires tool calling.
	if arr, ok := p.Tools.([]any); ok && len(arr) > 0 {
		req.RequiresTools = true
	}

	// Images: count image blocks so the estimate carries a fixed per-image
	// allowance (see imageTokenAllowance).
	req.ImageCount = countImages(p.Messages)
	req.HasImages = req.ImageCount > 0

	// Conservative input token estimate: prompt + flattened message text
	// (chars/4), plus the raw tool-schema payload when tools are present (the
	// schemas are sent to the engine and count against its context).
	prompt := p.Prompt
	for _, m := range p.Messages {
		prompt += stringContent(m.Content)
	}
	inputBytes := len(prompt)
	if req.RequiresTools && len(body) > 0 {
		inputBytes += toolsPayloadBytes(body)
	}
	req.InputTokens = inputBytes/4 + req.ImageCount*imageTokenAllowance

	req.RequiredContext = requiredContext(inputBytes, req.InputTokens, req.MaxOutput)
	return req
}

// maxZero clamps a negative requested value to 0: a negative max_tokens is
// malformed but parsable, and it must not drag the required context below the
// input estimate.
func maxZero(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

// toolsPayloadBytes returns the byte length of a non-null, non-empty "tools"
// array in the body, or 0 when the field is absent, null, empty, or the body
// is not a JSON object.
func toolsPayloadBytes(body []byte) int {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return 0
	}
	toolsRaw, ok := raw["tools"]
	if !ok {
		return 0
	}
	var arr []json.RawMessage
	if string(toolsRaw) == "null" {
		return 0
	}
	if err := json.Unmarshal(toolsRaw, &arr); err != nil || len(arr) == 0 {
		return 0
	}
	return len(toolsRaw)
}

// requiredContext computes the required context budget: input + requested
// output + a safety reserve. The reserve is the larger of a fixed floor and a
// small percent of the input, so a request is not routed to an endpoint whose
// max context cannot fit it.
func requiredContext(inputBytes, inputTokens, maxOutput int) int {
	reserve := contextReserveFloor
	if pct := int(math.Round(float64(inputBytes) * contextReservePct)); pct > reserve {
		reserve = pct
	}
	return inputTokens + maxOutput + reserve
}

// stringContent flattens a message content (string or block array) into a
// string length source for the token estimate. Block arrays contribute the
// text blocks' text values; image and other non-text blocks contribute no
// text length.
func stringContent(c any) string {
	switch v := c.(type) {
	case string:
		return v
	case []any:
		var out string
		for _, part := range v {
			m, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if s, ok := m["text"]; ok {
				if str, ok := s.(string); ok {
					out += str
				}
			}
		}
		return out
	default:
		return ""
	}
}

// messageContent is the minimal per-message shape needed to walk content.
type messageContent struct {
	Content any `json:"content"`
}

// countImages counts image content blocks across all messages: content-array
// blocks typed image / image_url / image_base64 / input_image, or a block that
// carries an image_url property without an explicit type.
func countImages(msgs []messageContent) int {
	count := 0
	for _, m := range msgs {
		arr, ok := m.Content.([]any)
		if !ok {
			continue
		}
		for _, part := range arr {
			pm, ok := part.(map[string]any)
			if !ok {
				continue
			}
			typ, _ := pm["type"].(string)
			if typ == "image" || typ == "image_url" || typ == "image_base64" || typ == "input_image" {
				count++
				continue
			}
			if _, has := pm["image_url"]; has {
				count++
			}
		}
	}
	return count
}
