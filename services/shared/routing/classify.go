// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"encoding/json"
	"errors"
	"unicode/utf8"
)

// ErrMalformedBody reports a non-empty request body that is not valid JSON for
// any recognised request shape. It lets a caller distinguish a client error (the
// request cannot be parsed, so it should get a 400 Bad Request) from a routing
// failure (the request is well-formed but no endpoint can serve it, a 503). An
// empty body is NOT malformed: it simply yields empty Requirements.
var ErrMalformedBody = errors.New("routing: malformed request body")

// ReservePolicy configures the conservative context reserves Classify adds when
// estimating RequiredContext. The zero value means "use the built-in defaults",
// so existing callers are unaffected. It exists because one universal output
// reserve is wrong for every deployment: a coding environment whose models emit
// long completions should be able to raise the output reserve far above the
// 512-token default without editing the routing core.
type ReservePolicy struct {
	// OutputReserveTokens is the output allowance added when a request does not
	// specify a maximum output length. A value <= 0 uses DefaultOutputReserveTokens.
	OutputReserveTokens int
	// ImageReserveTokensPerImage is the per-image context allowance. A value <= 0
	// uses DefaultImageTokenReservePerImage.
	ImageReserveTokensPerImage int
}

// outputReserve resolves the effective output reserve, saturating at the token
// cap so a pathological configuration cannot overflow the context arithmetic.
func (p ReservePolicy) outputReserve() int {
	if p.OutputReserveTokens > 0 {
		if p.OutputReserveTokens > maxTokenEstimate {
			return maxTokenEstimate
		}
		return p.OutputReserveTokens
	}
	return DefaultOutputReserveTokens
}

// imageReserve resolves the effective per-image reserve, saturating at the cap.
func (p ReservePolicy) imageReserve() int {
	if p.ImageReserveTokensPerImage > 0 {
		if p.ImageReserveTokensPerImage > maxTokenEstimate {
			return maxTokenEstimate
		}
		return p.ImageReserveTokensPerImage
	}
	return DefaultImageTokenReservePerImage
}

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
	// supported forms. It is the boolean view of ImageCount (ImageCount > 0).
	HasImages bool
	// ImageCount is the number of image parts detected across all supported forms
	// (content-block images, per-message Ollama images, and top-level Ollama
	// images). It drives a conservative per-image context reserve.
	ImageCount int
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

	// DefaultOutputReserveTokens is the conservative output allowance used when a
	// request does not specify a maximum output length. Treating unspecified
	// output as zero under-reserves context (a request whose input nearly fills a
	// window would be judged to fit when the completion cannot), so a single
	// central default is added instead. Tuned for safe routing, not billing.
	DefaultOutputReserveTokens = 512
	// DefaultImageTokenReservePerImage is a conservative per-image context
	// allowance. Exact image-token cost is backend/model specific; this is a safe
	// over-estimate so an image-heavy request is not routed to a too-small window.
	DefaultImageTokenReservePerImage = 1024
	// maxTokenEstimate caps every token estimate so a pathological request cannot
	// overflow int arithmetic and wrap to a small or negative required context.
	maxTokenEstimate = 1 << 30
)

// estimateTokensFromChars converts a character count to a conservative token
// estimate, rounding up so a single character still counts as at least one
// token. It saturates at maxTokenEstimate so a pathologically large input cannot
// overflow the multiplication.
func estimateTokensFromChars(chars int) int {
	if chars <= 0 {
		return 0
	}
	if chars > maxTokenEstimate {
		// tokens are always fewer than chars for this ratio, so capping the input
		// first both prevents overflow and keeps the result within the cap.
		chars = maxTokenEstimate
	}
	t := (chars*tokensPerCharNum + tokensPerCharDen - 1) / tokensPerCharDen
	if t > maxTokenEstimate {
		t = maxTokenEstimate
	}
	return t
}

// satAdd adds two non-negative token counts, saturating at maxTokenEstimate and
// never returning negative.
func satAdd(a, b int) int {
	if a < 0 {
		a = 0
	}
	if b < 0 {
		b = 0
	}
	s := a + b
	if s < a || s > maxTokenEstimate { // s < a detects wraparound
		return maxTokenEstimate
	}
	return s
}

// satMul multiplies two non-negative token counts, saturating at
// maxTokenEstimate.
func satMul(a, b int) int {
	if a <= 0 || b <= 0 {
		return 0
	}
	if a > maxTokenEstimate/b {
		return maxTokenEstimate
	}
	return a * b
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

// Classify extracts routing Requirements from a raw request body using the
// default reserve policy. It never panics and never reports malformed input: a
// nil, empty, malformed or unexpectedly-shaped body yields a zero-value
// Requirements (plus whatever fields did parse). It is retained for callers that
// cannot act on a client error; new callers on a request path that can return a
// status should prefer ClassifyRequest, which surfaces ErrMalformedBody so a
// malformed request becomes a 400 rather than a routing 503.
func Classify(body []byte) Requirements {
	req, _ := ClassifyRequest(body, ReservePolicy{})
	return req
}

// ClassifyRequest extracts routing Requirements under an explicit reserve policy
// and reports whether the body was parseable. It handles the OpenAI chat and
// completions shapes and the Ollama generate and chat shapes in a single pass,
// including string content, structured content blocks, every supported image
// form, tools/functions, streaming and the output-length fields.
//
// It returns ErrMalformedBody (with zero Requirements) when a non-empty body is
// not valid JSON for any recognised shape, so a request path can answer 400
// instead of forwarding unparseable bytes or collapsing into a false
// MODEL_NOT_AVAILABLE. An empty body is valid and yields empty Requirements.
func ClassifyRequest(body []byte, policy ReservePolicy) (Requirements, error) {
	var req Requirements
	if len(body) == 0 {
		return req, nil
	}
	var r rawRequest
	if err := json.Unmarshal(body, &r); err != nil {
		return req, ErrMalformedBody
	}

	req.Model = r.Model

	inputChars := 0
	imageCount := 0

	// Chat messages.
	for _, m := range r.Messages {
		req.InputTokensEst += perMessageOverhead
		inputChars += utf8.RuneCountInString(m.Role)
		text, imgs := decodeContent(m.Content)
		inputChars += utf8.RuneCountInString(text)
		imageCount += imgs
		imageCount += len(m.Images) // Ollama per-message base64 images
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
	imageCount += len(r.Images)

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

	req.InputTokensEst = satAdd(req.InputTokensEst, estimateTokensFromChars(inputChars))
	req.OutputTokensReq = resolveOutputTokens(r)
	req.ImageCount = imageCount
	req.HasImages = imageCount > 0
	req.RequestedContext = resolveRequestedContext(r)

	// Conservative required-context estimate. Unspecified/unbounded output uses a
	// default reserve rather than zero (never under-reserving), images add a
	// per-image reserve, and the whole thing is the larger of the estimated
	// conversation size and any explicitly requested window (num_ctx floor). All
	// arithmetic saturates so a pathological request can never wrap to a small or
	// negative value.
	effectiveOutput := req.OutputTokensReq
	if effectiveOutput <= 0 {
		effectiveOutput = policy.outputReserve()
	}
	imageReserve := satMul(req.ImageCount, policy.imageReserve())
	conversation := satAdd(satAdd(req.InputTokensEst, effectiveOutput), imageReserve)
	req.RequiredContext = conversation
	if req.RequestedContext > req.RequiredContext {
		req.RequiredContext = req.RequestedContext
	}
	return req, nil
}

// resolveRequestedContext extracts an explicitly requested context-window size
// (Ollama options.num_ctx). A non-positive value is treated as unspecified.
func resolveRequestedContext(r rawRequest) int {
	if r.Options != nil && r.Options.NumCtx != nil {
		v := clampNonNeg(*r.Options.NumCtx)
		if v > maxTokenEstimate {
			v = maxTokenEstimate // saturate an absurd requested window
		}
		return v
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

// decodeContent extracts the concatenated text and the number of image parts
// from a message "content" field, which may be a plain string or an array of
// structured blocks. It is total: any unexpected shape yields ("", 0) rather
// than an error or panic.
func decodeContent(raw json.RawMessage) (text string, images int) {
	if len(raw) == 0 {
		return "", 0
	}
	// String content.
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, 0
	}
	// Array of blocks.
	var blocks []json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return "", 0
	}
	var b []byte
	for _, blk := range blocks {
		t, img := decodeBlock(blk)
		b = append(b, t...)
		if img {
			images++
		}
	}
	return string(b), images
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
