// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// Target is how to reach one endpoint's backend and authenticate to it. It is
// resolved on the owning node from node-local state; Auth never crosses a node
// boundary. Crucially, a Target is produced by the caller's trusted resolver
// (Forward never lets request content choose a BaseURL), which is what keeps a
// client from steering PAIR at an arbitrary URL (SSRF).
type Target struct {
	// BaseURL is the backend origin, e.g. "http://127.0.0.1:11434". Path is
	// appended to it verbatim.
	BaseURL string
	// Auth carries node-local credentials for this specific endpoint.
	Auth LocalAuth
}

// TimeoutDefaults are the fallback durations used when a placement's Timeouts
// leave a field at zero. They let one endpoint's longer profile stay local to
// that endpoint without weakening the defaults every other endpoint inherits.
type TimeoutDefaults struct {
	ResponseHeader time.Duration
	FirstByte      time.Duration
}

// ForwardInput bundles everything Forward needs to execute a decision. The
// Transport is injectable so the whole forwarding path — capacity, failover,
// auth, model rewrite, timeouts, commit semantics — is testable with a fake
// RoundTripper and httptest.NewRecorder, with no socket and no server.
type ForwardInput struct {
	Method string
	// Path is the request path and query appended to each target's BaseURL.
	Path string
	// Header is the inbound request header set; it is cloned per attempt, has
	// hop-by-hop (including Connection-nominated) and client auth stripped, and
	// gets the endpoint's local auth applied.
	Header http.Header
	// Body is the fully buffered request body, replayed fresh on every attempt.
	Body []byte
	// Requirements is the classified request (used for the model rewrite target).
	Requirements Requirements
	// Ordered is the decision's policy-ordered eligible endpoints.
	Ordered []Placement
	// IsInference makes a 404 retryable (stale-inventory tolerance); for
	// non-inference requests a 404 is terminal.
	IsInference bool
	// Pools is the authoritative admission registry. A nil Pools is treated as
	// unbounded (no admission gate) rather than panicking.
	Pools *Pools
	// Transport performs the upstream round trip. A nil Transport (with no
	// TransportFor) yields a local 502 rather than a panic.
	Transport http.RoundTripper
	// TransportFor, when set, selects a per-endpoint transport (e.g. one with the
	// endpoint's connect timeout, from a TransportFactory) for each attempt. This
	// keeps one endpoint's dial timeout from affecting another's. A nil return
	// falls back to Transport.
	TransportFor func(pl Placement) http.RoundTripper
	// Getenv resolves env-backed auth values (defaults to a func returning "").
	Getenv func(string) string
	// Target resolves an endpoint id to its backend origin and local auth. A nil
	// resolver (or one returning false) yields a TARGET_UNAVAILABLE skip.
	Target func(endpointID string) (Target, bool)
	// Defaults supply timeout fallbacks.
	Defaults TimeoutDefaults
	// RewriteModel, when true, rewrites the outbound body's model field to the
	// placement's physical name. Left false for request shapes without a model
	// field.
	RewriteModel bool
}

// ForwardResult reports the outcome. It carries no request or response content.
type ForwardResult struct {
	// ServedEndpoint is the endpoint whose upstream response was committed to the
	// client (empty if none was, including when a local error was written).
	ServedEndpoint string
	Engine         string
	StatusCode     int
	// Committed is true once any byte (upstream OR a local error response) was
	// written to the client. Note the forwarder buffers upstream status+headers
	// until it has the first body byte (or a definitive empty body), so an
	// upstream response is only "committed" once its first byte is on the way to
	// the client — that is what makes first-byte failover possible.
	Committed bool
	// Attempts counts endpoints actually dialled (excludes capacity-full/no-target
	// skips).
	Attempts int
	// Rejections gathers execution-time exclusions (CAPACITY_FULL, timeouts,
	// connect failures, …) so the caller can build a full routing explanation
	// together with Decision.Rejected. It carries no request content or secrets.
	Rejections []Rejection
	// NoEligible is true when there was nothing eligible to try (Ordered empty).
	NoEligible bool
}

// hopByHop are the always-stripped hop-by-hop headers (RFC 7230 §6.1). In
// addition, any header NAMED in the request's Connection header is also stripped
// (see sanitizedHeader) — a classic reverse-proxy correctness/security point.
var hopByHop = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

// firstChunkSize bounds the first read used to detect the first output byte.
const firstChunkSize = 32 * 1024

// Forward executes the decision against upstream backends, writing the first
// committed response to w.
//
// Guarantees:
//   - Capacity is reserved (authoritatively) before each dial and released
//     exactly once on every terminal path of that attempt — retry, non-retry
//     commit, stream completion, stream error, client/context cancellation and
//     timeout — via the pool's idempotent release. A nil Pools is unbounded.
//   - A failed reservation is honoured: that endpoint is skipped with a
//     CAPACITY_FULL rejection and never dialled.
//   - Failover happens only before the first byte reaches the client. Retryable
//     conditions: transport/dial error, a retryable status (408/429/500/502/503/
//     504, plus 404 for inference) and a first-byte timeout. Any other status —
//     including 400/401/422 and non-transient 5xx like 501/505 — is forwarded
//     as-is.
//   - A first-byte timeout NEVER commits an upstream 200-with-empty-body, even on
//     the final candidate: since nothing has been written to the client yet, a
//     local 504 is returned instead.
//   - Once the client write begins the response is committed; nothing after is
//     retried.
//   - If the client's context is cancelled, the loop stops rather than failing
//     over to more endpoints, and any in-flight reservation is released.
//   - If nothing was eligible/admittable/servable, a local error is written
//     (504 for timeouts, 502 for connect failures, 503 otherwise). The request
//     is never forwarded to an ineligible endpoint.
func Forward(ctx context.Context, w http.ResponseWriter, in *ForwardInput) ForwardResult {
	var res ForwardResult
	if len(in.Ordered) == 0 {
		res.NoEligible = true
		writeLocalError(w, http.StatusServiceUnavailable, "no eligible endpoint for request")
		res.Committed = true
		res.StatusCode = http.StatusServiceUnavailable
		return res
	}
	if in.Transport == nil && in.TransportFor == nil {
		writeLocalError(w, http.StatusBadGateway, "no upstream transport configured")
		res.Committed = true
		res.StatusCode = http.StatusBadGateway
		return res
	}
	getenv := in.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}

	lastReason := ReasonNone
	record := func(id, engine string, r Reason) {
		lastReason = r
		res.Rejections = append(res.Rejections, Rejection{EndpointID: id, Engine: engine, Reason: r})
	}

	for i, pl := range in.Ordered {
		if ctx.Err() != nil {
			// Client went away; do not fan the request out further.
			return res
		}
		release, ok := reserveOn(in.Pools, pl.EndpointID, pl.Capacity)
		if !ok {
			record(pl.EndpointID, pl.Engine, ReasonCapacityFull)
			continue
		}

		target, hasTarget := resolveTarget(in.Target, pl.EndpointID)
		if !hasTarget {
			release()
			record(pl.EndpointID, pl.Engine, ReasonTargetUnavailable)
			continue
		}

		rt := pickTransport(in, pl)
		if rt == nil {
			release()
			record(pl.EndpointID, pl.Engine, ReasonConnectFailed)
			continue
		}

		res.Attempts++
		isLast := i == len(in.Ordered)-1
		disp := attempt(ctx, w, in, pl, target, getenv, isLast, rt)

		if disp.committed {
			// Reservation was held for the full stream; release now, exactly once.
			release()
			res.ServedEndpoint = pl.EndpointID
			res.Engine = pl.Engine
			res.StatusCode = disp.status
			res.Committed = true
			return res
		}

		// Not committed: release and either fail over or stop.
		release()
		if disp.reason != ReasonNone {
			record(pl.EndpointID, pl.Engine, disp.reason)
		}
		if ctx.Err() != nil {
			return res
		}
		// fall through to the next candidate
	}

	// Exhausted every candidate without committing a response. Choose a local
	// status that reflects why (timeout vs connect vs capacity).
	if !res.Committed {
		status, msg := statusForReason(lastReason)
		writeLocalError(w, status, msg)
		res.Committed = true
		res.StatusCode = status
	}
	return res
}

// reserveOn reserves against a pool registry, treating a nil registry as
// unbounded (a no-op release, always admitted) so a partially-wired integration
// cannot panic on the hot path.
func reserveOn(pools *Pools, id string, capacity int) (func(), bool) {
	if pools == nil {
		return func() {}, true
	}
	return pools.Reserve(id, capacity)
}

// disposition is one attempt's outcome.
type disposition struct {
	committed bool
	status    int
	reason    Reason // non-None when this attempt failed for a recordable reason
}

// attempt performs one endpoint attempt: builds the outbound request (model
// rewrite, header sanitisation, local auth), round-trips it under a
// response-header deadline, applies the retry predicate, and — when committing —
// streams the body under a first-byte deadline. It never writes to w unless it
// commits, and it never commits an upstream response that has not produced a
// first byte.
func attempt(ctx context.Context, w http.ResponseWriter, in *ForwardInput, pl Placement, target Target, getenv func(string) string, isLast bool, rt http.RoundTripper) disposition {
	body := in.Body
	if in.RewriteModel && pl.Physical != "" {
		if rewritten, changed := RewriteModel(body, pl.Physical); changed {
			body = rewritten
		}
	}

	attemptCtx, cancel := context.WithCancel(ctx)
	committed := false
	defer func() {
		if !committed {
			cancel()
		}
	}()

	req, err := http.NewRequestWithContext(attemptCtx, in.Method, target.BaseURL+in.Path, bytes.NewReader(body))
	if err != nil {
		// Malformed BaseURL/method: this endpoint is unusable, not a client error.
		return disposition{reason: ReasonTargetUnavailable}
	}
	req.Header = sanitizedHeader(in.Header)
	StripClientAuth(req.Header)
	if !target.Auth.Empty() {
		if aerr := ApplyAuth(req.Header, target.Auth, getenv); aerr != nil {
			return disposition{reason: ReasonAuthUnavailable}
		}
	}
	req.ContentLength = int64(len(body))

	headerTimeout := pl.Timeouts.ResponseHeader(in.Defaults.ResponseHeader)
	resp, rtReason := roundTripWithHeaderTimeout(attemptCtx, cancel, rt, req, headerTimeout)
	if resp == nil {
		return disposition{reason: rtReason}
	}

	// Retry decision from the status line, before any client write.
	if shouldRetryStatus(resp.StatusCode, in.IsInference) && !isLast {
		resp.Body.Close()
		return disposition{reason: reasonForStatus(resp.StatusCode)}
	}

	// A nil body (a misbehaving transport) is treated as an immediate empty body.
	if resp.Body == nil {
		committed = true
		writeResponseHead(w, resp)
		cancel()
		return disposition{committed: true, status: resp.StatusCode}
	}

	// First-byte deadline for the first read. A timeout here NEVER commits: an
	// upstream that returned 200 headers but no body byte must not become a
	// 200-with-empty-body to the client. Since nothing is written yet, we fail
	// over (if not last) or let the caller write a local 504 (if last).
	firstByte := pl.Timeouts.FirstByte(in.Defaults.FirstByte)
	first, ferr, timedOut := readFirst(attemptCtx, cancel, resp.Body, firstByte)
	if timedOut {
		resp.Body.Close()
		return disposition{reason: ReasonFirstByteTimeout}
	}

	// Commit: write status+headers, then the first chunk and the remainder.
	committed = true
	writeResponseHead(w, resp)
	status := resp.StatusCode
	if len(first) > 0 {
		if _, werr := w.Write(first); werr != nil {
			resp.Body.Close()
			cancel()
			return disposition{committed: true, status: status}
		}
		flush(w)
	}
	// ferr != nil (e.g. io.EOF) means the body ended at/with the first read —
	// a legitimate empty or single-chunk body — so there is nothing more to copy.
	if ferr == nil {
		copyStream(attemptCtx, w, resp.Body)
	}
	resp.Body.Close()
	cancel()
	return disposition{committed: true, status: status}
}

// rtResult is one round-trip's outcome, at package scope so drain can reference
// the channel element type by name.
type rtResult struct {
	resp *http.Response
	err  error
}

// roundTripWithHeaderTimeout runs rt.RoundTrip(req) and races it against a
// response-header deadline, distinguishing client cancellation, a header timeout
// and a transport/connect failure. A late response arriving after a timeout is
// drained and closed in the background so no connection leaks. This relies on
// the transport honouring request-context cancellation (standard net/http does);
// a transport that ignores context forever would leak this goroutine (documented
// limitation).
func roundTripWithHeaderTimeout(ctx context.Context, cancel context.CancelFunc, rt http.RoundTripper, req *http.Request, timeout time.Duration) (*http.Response, Reason) {
	ch := make(chan rtResult, 1)
	go func() {
		resp, err := rt.RoundTrip(req)
		ch <- rtResult{resp, err}
	}()

	var timer *time.Timer
	var timerC <-chan time.Time
	if timeout > 0 {
		timer = time.NewTimer(timeout)
		timerC = timer.C
	}
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	select {
	case <-ctx.Done():
		cancel()
		go drain(ch)
		return nil, ReasonClientCancelled
	case <-timerC:
		cancel()
		go drain(ch)
		return nil, ReasonHeaderTimeout
	case r := <-ch:
		if r.err != nil {
			if r.resp != nil {
				r.resp.Body.Close()
			}
			return nil, ReasonConnectFailed
		}
		return r.resp, ReasonNone
	}
}

// drain consumes and closes a late round-trip result.
func drain(ch <-chan rtResult) {
	r := <-ch
	if r.resp != nil {
		r.resp.Body.Close()
	}
}

// readFirst reads the first chunk of body under a first-byte deadline. It
// returns the bytes read, the read error (io.EOF is normal for an empty body),
// and whether the deadline (or context) fired before any byte arrived. On
// timeout it cancels the request context; the caller closes the body, which
// unblocks the read goroutine of a compliant body so it cannot leak.
func readFirst(ctx context.Context, cancel context.CancelFunc, body io.Reader, timeout time.Duration) (data []byte, err error, timedOut bool) {
	type readResult struct {
		n   int
		err error
	}
	buf := make([]byte, firstChunkSize)
	ch := make(chan readResult, 1)
	go func() {
		n, e := body.Read(buf)
		ch <- readResult{n, e}
	}()

	var timerC <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		timerC = timer.C
	}

	select {
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err(), true
	case <-timerC:
		cancel()
		return nil, context.DeadlineExceeded, true
	case r := <-ch:
		return buf[:r.n], r.err, false
	}
}

// copyStream copies the remaining body to the client, flushing after each chunk.
// It returns promptly on context cancellation; the caller then closes the body,
// which unblocks a compliant body's Read so the copy goroutine exits. A body
// that honours neither context nor Close cannot be force-terminated in Go
// (documented); capacity is released by the caller regardless of this goroutine.
func copyStream(ctx context.Context, w http.ResponseWriter, body io.Reader) {
	done := make(chan struct{})
	go func() {
		buf := make([]byte, firstChunkSize)
		for {
			n, err := body.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					break
				}
				flush(w)
			}
			if err != nil {
				break
			}
		}
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// resolveTarget looks up an endpoint's target, tolerating a nil resolver.
func resolveTarget(f func(string) (Target, bool), id string) (Target, bool) {
	if f == nil {
		return Target{}, false
	}
	return f(id)
}

// pickTransport selects the round tripper for one attempt: a per-endpoint one
// (TransportFor) when provided and non-nil, else the shared Transport.
func pickTransport(in *ForwardInput, pl Placement) http.RoundTripper {
	if in.TransportFor != nil {
		if rt := in.TransportFor(pl); rt != nil {
			return rt
		}
	}
	return in.Transport
}

// sanitizedHeader clones h, strips the standard hop-by-hop headers AND any
// header named in the request's Connection header (RFC 7230 §6.1), so a client
// cannot smuggle a header past the proxy by nominating it in Connection.
func sanitizedHeader(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, v := range h {
		out[k] = append([]string(nil), v...)
	}
	// Remove headers nominated in Connection before removing Connection itself.
	for _, cv := range h.Values("Connection") {
		for _, tok := range splitConnectionTokens(cv) {
			if tok != "" {
				out.Del(tok)
			}
		}
	}
	for _, hb := range hopByHop {
		out.Del(hb)
	}
	return out
}

// splitConnectionTokens splits a Connection header value on commas and trims
// surrounding whitespace from each token.
func splitConnectionTokens(v string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(v); i++ {
		if i == len(v) || v[i] == ',' {
			tok := v[start:i]
			// trim ASCII spaces/tabs
			for len(tok) > 0 && (tok[0] == ' ' || tok[0] == '\t') {
				tok = tok[1:]
			}
			for len(tok) > 0 && (tok[len(tok)-1] == ' ' || tok[len(tok)-1] == '\t') {
				tok = tok[:len(tok)-1]
			}
			out = append(out, tok)
			start = i + 1
		}
	}
	return out
}

// writeResponseHead copies the upstream response's headers (minus hop-by-hop)
// and status to the client.
func writeResponseHead(w http.ResponseWriter, resp *http.Response) {
	dst := w.Header()
	for k, v := range resp.Header {
		if isHopByHop(k) {
			continue
		}
		dst[k] = append([]string(nil), v...)
	}
	w.WriteHeader(resp.StatusCode)
}

func isHopByHop(k string) bool {
	ck := http.CanonicalHeaderKey(k)
	for _, hb := range hopByHop {
		if ck == hb {
			return true
		}
	}
	return false
}

func flush(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// shouldRetryStatus is PAIR's deliberate failover predicate. Retryable:
// 408 (request timeout), 429 (too many requests), and the transient gateway/
// server codes 500/502/503/504; plus 404 for inference (stale inventory). Every
// other status is terminal and forwarded as-is: 400/401/403/409/422/423/425 are
// client/state errors that would fail identically elsewhere, and non-transient
// 5xx like 501 (not implemented) and 505 (version not supported) likewise. This
// avoids wasting the candidate list on errors another identical backend would
// also return, while never sleeping on the hot path (Retry-After is not honoured
// as a delay; PAIR moves to the next eligible endpoint immediately).
func shouldRetryStatus(code int, isInference bool) bool {
	switch code {
	case http.StatusRequestTimeout, // 408
		http.StatusTooManyRequests,     // 429
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		http.StatusGatewayTimeout:      // 504
		return true
	case http.StatusNotFound: // 404
		return isInference
	default:
		return false
	}
}

// reasonForStatus maps a retryable status to an accurate recordable reason so a
// routing trace distinguishes a stale-inventory 404, a non-5xx retryable status
// (408/429), and a genuine upstream server error (5xx). A 408 request timeout is
// NOT a server error and must never be recorded as UPSTREAM_5XX.
func reasonForStatus(code int) Reason {
	switch code {
	case http.StatusNotFound: // 404 (inference stale inventory)
		return ReasonModelNotAvailable
	case http.StatusRequestTimeout, // 408
		http.StatusTooManyRequests: // 429
		return ReasonUpstreamRetryableStatus
	default: // 500/502/503/504
		return ReasonUpstream5xx
	}
}

// statusForReason chooses the local error status when no candidate committed a
// response, so the client sees a code that reflects the actual failure mode.
func statusForReason(r Reason) (int, string) {
	switch r {
	case ReasonHeaderTimeout, ReasonFirstByteTimeout:
		return http.StatusGatewayTimeout, "no endpoint produced a response before its timeout"
	case ReasonConnectFailed:
		return http.StatusBadGateway, "no upstream endpoint could be reached"
	case ReasonCapacityFull:
		return http.StatusServiceUnavailable, "all eligible endpoints are at capacity"
	default:
		return http.StatusServiceUnavailable, "no endpoint could serve the request"
	}
}

// writeLocalError writes a minimal JSON error to the client. It contains no
// request content, no secrets and no upstream detail.
func writeLocalError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    "routing_error",
		},
	})
}
