// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"encoding/json"
	"unicode/utf8"
)

// Requirements is the routing-relevant summary of a request. It contains only
// metadata used for eligibility and admission — never the request's actual
// content — so it is safe to keep in routing diagnostics.
type Requirements struct {
	// Model is the requested (logical or physical) model name.
	Model string
	// APIFamily is the wire contract of the interface the request arrived on.
	// Classify does not set it (a request body does not declare its family); the
	// caller sets it from the listening surface. APIFamilyUnknown disables the
	// family gate.
	APIFamily APIFamily
	// HasImages is true when any message/content carries an image in any of the
	// supported forms.
	HasImages bool
	// RequiresTools is true when the request declares one or more tools
	// (or legacy functions).
	RequiresTools bool
	// RequiresStream is true when the request asked for a streamed response.
	RequiresStream bool
	// InputTokensEst is a conservative (deliberately high) estimate of the input
	// token count across all text content, roles and tool schemas.
	InputTokensEst int
	// OutputTokensReq is the requested maximum output tokens (0 when unspecified
	// or specified as unbounded/negative).
	OutputTokensReq int
	// RequestedContext is a context-window size the request explicitly asked for
	// (Ollama options.num_ctx). Zero means unspecified. An endpoint whose model
	// declares a smaller window cannot honour it, so it acts as a floor on
	// RequiredContext independent of the estimated conversation size.
	RequestedContext int
	// RequiredContext is the smallest context window an endpoint must advertise
	// to be eligible: the larger of the estimated conversation size
	// (InputTokensEst + OutputTokensReq) and any explicitly RequestedContext.
	RequiredContext int
}

// charsPerTokenNumerator/Denominator express the conservative chars->tokens
// ratio as a rational (10/33 ≈ 3.3 chars per token). Real tokenizers average
// closer to 4 chars/token for English, so dividing by ~3.3 deliberately
// OVER-estimates input tokens. Over-estimation is the safe direction for
// context gating: we would rather reject a borderline endpoint than overflow a
// too-small context window. Perfect tokenizer precision is explicitly not a
// goal (see docs); a safe conservative estimate is.
const (
	tokensPerCharNum = 10
	tokensPerCharDen = 33
	// perMessageOverhead accounts for chat framing tokens (role markers,
	// separators) added per message by chat templates.
	perMessageOverhead = 4
)

// estimateTokensFromChars converts a character count to a conservative token
// estimate, rounding up so a single character still counts as at least one
// token.
func estimateTokensFromChars(chars int) int {
	if chars <= 0 {
		return 0
	}
	// ceil(chars * num / den)
	return (chars*tokensPerCharNum + tokensPerCharDen - 1) / tokensPerCharDen
}

// rawRequest is the permissive superset of the OpenAI and Ollama request shapes
// Classify understands. Every flexible field is json.RawMessage or a pointer so
// a missing, null or unexpectedly-typed field decodes without error.
type rawRequest struct {
	Model               string            `json:"model"`
	Messages            []rawMessage      `json:"messages"`
	Prompt              json.RawMessage   `json:"prompt"`
	System              json.RawMessage   `json:"system"`
	Suffix              json.RawMessage   `json:"suffix"`
	Images              []json.RawMessage `json:"images"`
	Tools               []json.RawMessage `json:"tools"`
	Functions           []json.RawMessage `json:"functions"`
	Stream              *bool             `json:"stream"`
	MaxTokens           *int              `json:"max_tokens"`
	MaxCompletionTokens *int              `json:"max_completion_tokens"`
	NumPredict          *int              `json:"num_predict"`
	Options             *rawOptions       `json:"options"`
}

type rawOptions struct {
	NumPredict *int `json:"num_predict"`
	NumCtx     *int `json:"num_ctx"`
}

type rawMessage struct {
	Role      string            `json:"role"`
	Content   json.RawMessage   `json:"content"`
	Images    []json.RawMessage `json:"images"`
	ToolCalls []json.RawMessage `json:"tool_calls"`
}

// Classify extracts routing Requirements from a raw request body. It never
// panics: a nil, empty, malformed or unexpectedly-shaped body yields a
// zero-value Requirements (plus whatever fields did parse). It handles the
// OpenAI chat and completions shapes and the Ollama generate and chat shapes in
// a single pass, including string content, structured content blocks, every
// supported image form, tools/functions, streaming and the output-length
// fields.
func Classify(body []byte) Requirements {
	var req Requirements
	if len(body) == 0 {
		return req
	}
	var r rawRequest
	if err := json.Unmarshal(body, &r); err != nil {
		// A malformed body is not fatal to routing: we simply learn nothing from
		// it. The caller still has the requested model from its own path parsing
		// if it needs one.
		return req
	}

	req.Model = r.Model

	inputChars := 0

	// Chat messages.
	for _, m := range r.Messages {
		req.InputTokensEst += perMessageOverhead
		inputChars += utf8.RuneCountInString(m.Role)
		text, hasImg := decodeContent(m.Content)
		inputChars += utf8.RuneCountInString(text)
		if hasImg {
			req.HasImages = true
		}
		if len(m.Images) > 0 {
			req.HasImages = true
		}
		if len(m.ToolCalls) > 0 {
			// Assistant tool-call history implies a tool-using conversation.
			req.RequiresTools = true
		}
	}

	// Completions/generate prompt (string or []string) and Ollama system/suffix.
	inputChars += rawTextChars(r.Prompt)
	inputChars += rawTextChars(r.System)
	inputChars += rawTextChars(r.Suffix)

	// Top-level Ollama images.
	if len(r.Images) > 0 {
		req.HasImages = true
	}

	// Tools / functions: both presence (gate) and size (context cost).
	if len(r.Tools) > 0 || len(r.Functions) > 0 {
		req.RequiresTools = true
	}
	for _, t := range r.Tools {
		inputChars += len(t) // raw JSON byte length is a fair proxy for schema size
	}
	for _, f := range r.Functions {
		inputChars += len(f)
	}

	if r.Stream != nil && *r.Stream {
		req.RequiresStream = true
	}

	req.InputTokensEst += estimateTokensFromChars(inputChars)
	req.OutputTokensReq = resolveOutputTokens(r)
	req.RequestedContext = resolveRequestedContext(r)
	// The required window is the larger of the estimated conversation size and
	// an explicitly requested context window: an endpoint must satisfy both.
	req.RequiredContext = max(req.InputTokensEst+req.OutputTokensReq, req.RequestedContext)
	return req
}

// resolveRequestedContext extracts an explicitly requested context-window size
// (Ollama options.num_ctx). A non-positive value is treated as unspecified.
func resolveRequestedContext(r rawRequest) int {
	if r.Options != nil && r.Options.NumCtx != nil {
		return clampNonNeg(*r.Options.NumCtx)
	}
	return 0
}

// resolveOutputTokens applies the output-length precedence:
// max_completion_tokens > max_tokens > top-level num_predict > options.num_predict.
// A value <= 0 (unset, zero, or an Ollama "unbounded" sentinel like -1/-2) is
// treated as 0: we cannot bound the output, so it contributes nothing to the
// additive required-context estimate rather than corrupting it with a negative.
func resolveOutputTokens(r rawRequest) int {
	switch {
	case r.MaxCompletionTokens != nil:
		return clampNonNeg(*r.MaxCompletionTokens)
	case r.MaxTokens != nil:
		return clampNonNeg(*r.MaxTokens)
	case r.NumPredict != nil:
		return clampNonNeg(*r.NumPredict)
	case r.Options != nil && r.Options.NumPredict != nil:
		return clampNonNeg(*r.Options.NumPredict)
	default:
		return 0
	}
}

func clampNonNeg(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// rawTextChars returns the rune count of text carried by a field that may be a
// JSON string or a JSON array of strings, and 0 for anything else (null,
// object, number, malformed).
func rawTextChars(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return utf8.RuneCountInString(s)
	}
	var arr []string
	if json.Unmarshal(raw, &arr) == nil {
		n := 0
		for _, x := range arr {
			n += utf8.RuneCountInString(x)
		}
		return n
	}
	return 0
}

// contentBlock is one structured content element. Every image-bearing field is
// captured so an image is detected regardless of which convention the client
// used.
type contentBlock struct {
	Type        string          `json:"type"`
	Text        string          `json:"text"`
	ImageURL    json.RawMessage `json:"image_url"`
	Image       json.RawMessage `json:"image"`
	InputImage  json.RawMessage `json:"input_image"`
	ImageBase64 json.RawMessage `json:"image_base64"`
	Source      json.RawMessage `json:"source"`
}

// decodeContent extracts the concatenated text and whether any image is present
// from a message "content" field, which may be a plain string or an array of
// structured blocks. It is total: any unexpected shape yields ("", false)
// rather than an error or panic.
func decodeContent(raw json.RawMessage) (text string, hasImage bool) {
	if len(raw) == 0 {
		return "", false
	}
	// String content.
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, false
	}
	// Array of blocks.
	var blocks []json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return "", false
	}
	var b []byte
	for _, blk := range blocks {
		t, img := decodeBlock(blk)
		b = append(b, t...)
		if img {
			hasImage = true
		}
	}
	return string(b), hasImage
}

// decodeBlock returns a single content block's text and whether it is an image
// block. A block may also be a bare string (some clients send mixed arrays).
func decodeBlock(raw json.RawMessage) (text string, isImage bool) {
	if len(raw) == 0 {
		return "", false
	}
	// Bare string element.
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, false
	}
	var blk contentBlock
	if json.Unmarshal(raw, &blk) != nil {
		return "", false
	}
	// An image is detected by type OR by the presence of any image-bearing
	// field, so a block that omits/mis-spells "type" but carries an image_url is
	// still caught.
	switch blk.Type {
	case "image_url", "image", "input_image", "image_base64":
		isImage = true
	}
	if len(blk.ImageURL) > 0 || len(blk.Image) > 0 || len(blk.InputImage) > 0 || len(blk.ImageBase64) > 0 {
		isImage = true
	}
	// Anthropic-style image blocks carry {"type":"image","source":{...}}.
	if blk.Type == "image" && len(blk.Source) > 0 {
		isImage = true
	}
	return blk.Text, isImage
}
