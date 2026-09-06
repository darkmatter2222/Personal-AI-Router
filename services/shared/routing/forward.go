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
// boundary.
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
	// hop-by-hop and client auth stripped, and gets the endpoint's local auth
	// applied.
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
	// Pools is the authoritative admission registry.
	Pools *Pools
	// Transport performs the upstream round trip.
	Transport http.RoundTripper
	// Getenv resolves env-backed auth values (defaults to a func returning "").
	Getenv func(string) string
	// Target resolves an endpoint id to its backend origin and local auth.
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
	// ServedEndpoint is the endpoint whose response was committed to the client
	// (empty if none was).
	ServedEndpoint string
	Engine         string
	StatusCode     int
	// Committed is true once any response byte (headers count) was written to the
	// client; after that no failover is possible.
	Committed bool
	// Attempts counts endpoints actually dialled (excludes capacity-full skips).
	Attempts int
	// Rejections gathers execution-time exclusions (e.g. CAPACITY_FULL) so the
	// caller can build a full routing explanation together with Decision.Rejected.
	Rejections []Rejection
	// NoEligible is true when there was nothing eligible to try (Ordered empty).
	NoEligible bool
}

// hopByHop are the headers a proxy must not forward.
var hopByHop = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

// firstChunkSize bounds the first read used to detect the first output byte.
const firstChunkSize = 32 * 1024

// Forward executes the decision against upstream backends, writing the first
// committed response to w. Invariants it guarantees:
//
//   - Capacity is reserved (authoritatively) before each dial and released
//     exactly once on every terminal path of that attempt — retry, non-retry
//     commit, stream completion, stream error, client/context cancellation and
//     timeout — via the pool's idempotent release.
//   - A failed reservation is honoured: that endpoint is skipped with a
//     CAPACITY_FULL rejection and never dialled.
//   - Failover happens only before the first byte reaches the client. Retryable
//     conditions are a transport/dial error, a retryable status
//     (408/429/502/503/504, any 5xx, and 404 for inference) and a first-byte
//     timeout. A non-retryable status (e.g. 400/401) is forwarded as-is.
//   - Once the client write begins the response is committed; nothing after is
//     retried.
//   - If the client's context is cancelled, the loop stops rather than failing
//     over to more endpoints.
//   - If nothing was eligible or nothing could be admitted/served, a local
//     no-eligible/unavailable response is written; the request is never
//     forwarded to an ineligible endpoint.
func Forward(ctx context.Context, w http.ResponseWriter, in *ForwardInput) ForwardResult {
	var res ForwardResult
	if len(in.Ordered) == 0 {
		res.NoEligible = true
		writeLocalError(w, http.StatusServiceUnavailable, "no eligible endpoint for request")
		res.Committed = true
		res.StatusCode = http.StatusServiceUnavailable
		return res
	}
	getenv := in.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}

	for i, pl := range in.Ordered {
		if ctx.Err() != nil {
			// Client went away; do not fan the request out further.
			return res
		}
		release, ok := in.Pools.Reserve(pl.EndpointID, pl.Capacity)
		if !ok {
			res.Rejections = append(res.Rejections, Rejection{
				EndpointID: pl.EndpointID, Engine: pl.Engine, Reason: ReasonCapacityFull,
			})
			continue
		}

		target, hasTarget := resolveTarget(in.Target, pl.EndpointID)
		if !hasTarget {
			release()
			res.Rejections = append(res.Rejections, Rejection{
				EndpointID: pl.EndpointID, Engine: pl.Engine, Reason: ReasonEndpointUnhealthy,
			})
			continue
		}

		res.Attempts++
		isLast := i == len(in.Ordered)-1
		disp := attempt(ctx, w, in, pl, target, getenv, isLast)

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
			res.Rejections = append(res.Rejections, Rejection{
				EndpointID: pl.EndpointID, Engine: pl.Engine, Reason: disp.reason,
			})
		}
		if ctx.Err() != nil {
			return res
		}
		// fall through to the next candidate
	}

	// Exhausted every candidate without committing a response.
	if !res.Committed {
		writeLocalError(w, http.StatusServiceUnavailable, "no endpoint could serve the request")
		res.Committed = true
		res.StatusCode = http.StatusServiceUnavailable
	}
	return res
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
// commits.
func attempt(ctx context.Context, w http.ResponseWriter, in *ForwardInput, pl Placement, target Target, getenv func(string) string, isLast bool) disposition {
	body := in.Body
	if in.RewriteModel && pl.Physical != "" {
		if rewritten, changed := RewriteModel(body, pl.Physical); changed {
			body = rewritten
		}
	}

	attemptCtx, cancel := context.WithCancel(ctx)
	// cancel is invoked on every exit path below (either explicitly on failover
	// or via the committed-stream cleanup). Guard with defer as a backstop so an
	// early return can never leak the context.
	committed := false
	defer func() {
		if !committed {
			cancel()
		}
	}()

	req, err := http.NewRequestWithContext(attemptCtx, in.Method, target.BaseURL+in.Path, bytes.NewReader(body))
	if err != nil {
		return disposition{reason: ReasonEndpointUnhealthy}
	}
	req.Header = sanitizedHeader(in.Header)
	StripClientAuth(req.Header)
	if !target.Auth.Empty() {
		if aerr := ApplyAuth(req.Header, target.Auth, getenv); aerr != nil {
			// Cannot authenticate to this backend locally; treat as this
			// endpoint being unusable and fail over.
			return disposition{reason: ReasonAuthUnavailable}
		}
	}
	req.ContentLength = int64(len(body))

	headerTimeout := pl.Timeouts.ResponseHeader(in.Defaults.ResponseHeader)
	resp, rtReason := roundTripWithHeaderTimeout(attemptCtx, cancel, in.Transport, req, headerTimeout)
	if resp == nil {
		return disposition{reason: rtReason}
	}

	// Retry decision from the status line, before any client write.
	if shouldRetryStatus(resp.StatusCode, in.IsInference) && !isLast {
		resp.Body.Close()
		return disposition{reason: reasonForStatus(resp.StatusCode)}
	}

	// Commit path: stream the body, bounded by the first-byte deadline for the
	// first read.
	firstByte := pl.Timeouts.FirstByte(in.Defaults.FirstByte)
	first, ferr, timedOut := readFirst(attemptCtx, cancel, resp.Body, firstByte)
	if timedOut && !isLast {
		resp.Body.Close()
		return disposition{reason: ReasonEndpointUnhealthy}
	}

	// From here we commit to this endpoint and write to the client.
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
	// Stream the remainder. ferr already terminal (EOF/timeout/err) means nothing
	// more to copy.
	if ferr == nil && !timedOut {
		copyStream(w, resp.Body)
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
// response-header deadline. On timeout it cancels the in-flight request (via the
// provided cancel) and returns a nil response with an unhealthy reason; on a
// transport error it returns a nil response with an unhealthy reason; otherwise
// it returns the response. A late response arriving after a timeout is drained
// and closed in the background so no connection leaks.
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
		return nil, ReasonEndpointUnhealthy
	case <-timerC:
		cancel()
		go drain(ch)
		return nil, ReasonEndpointUnhealthy
	case r := <-ch:
		if r.err != nil {
			if r.resp != nil {
				r.resp.Body.Close()
			}
			return nil, ReasonEndpointUnhealthy
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
// and whether the deadline fired before any byte arrived. On timeout it cancels
// the request context and closes nothing (the caller closes body).
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

// copyStream copies the remaining body to the client, flushing after each
// chunk so streamed tokens are delivered promptly. Errors (including a dead
// client) end the copy; the reservation is released by the caller regardless.
func copyStream(w http.ResponseWriter, body io.Reader) {
	buf := make([]byte, firstChunkSize)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			flush(w)
		}
		if err != nil {
			return
		}
	}
}

// resolveTarget looks up an endpoint's target, tolerating a nil resolver.
func resolveTarget(f func(string) (Target, bool), id string) (Target, bool) {
	if f == nil {
		return Target{}, false
	}
	return f(id)
}

// sanitizedHeader clones h and strips hop-by-hop headers.
func sanitizedHeader(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, v := range h {
		out[k] = append([]string(nil), v...)
	}
	for _, hb := range hopByHop {
		out.Del(hb)
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
	for _, hb := range hopByHop {
		if http.CanonicalHeaderKey(k) == hb {
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

// shouldRetryStatus mirrors PAIR's existing failover predicate: 408/429/502/503/
// 504 and any 5xx are retryable; 404 is retryable only for inference (stale
// inventory tolerance); every other 4xx (400/401/422/…) is terminal because it
// would fail identically on every candidate.
func shouldRetryStatus(code int, isInference bool) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	case http.StatusNotFound:
		return isInference
	}
	return code >= 500
}

// reasonForStatus maps a retryable status to a recordable reason.
func reasonForStatus(code int) Reason {
	switch code {
	case http.StatusServiceUnavailable:
		return ReasonEndpointUnhealthy
	case http.StatusNotFound:
		return ReasonModelNotAvailable
	default:
		return ReasonEndpointUnhealthy
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
