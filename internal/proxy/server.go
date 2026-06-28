package proxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"router-eval/internal/artifacts"
	"router-eval/internal/parsers"
	"router-eval/internal/routers"
)

const schemaVersion = 1

type Config struct {
	Addr        string
	Upstream    string
	OutDir      string
	RunID       string
	Router      routers.Adapter
	HTTPClient  *http.Client
	ProxyAPIKey string
}

type Server struct {
	config   Config
	upstream *url.URL
	store    *artifacts.Store
	http     *http.Server
	client   *http.Client
}

func NewServer(config Config) (*Server, error) {
	if config.Router == nil {
		return nil, errors.New("router adapter is required")
	}
	if config.Addr == "" {
		config.Addr = "127.0.0.1:8080"
	}
	if config.Upstream == "" {
		return nil, errors.New("upstream URL is required")
	}
	upstream, err := url.Parse(config.Upstream)
	if err != nil {
		return nil, err
	}
	if upstream.Scheme == "" || upstream.Host == "" {
		return nil, fmt.Errorf("invalid upstream URL %q", config.Upstream)
	}
	store, err := artifacts.NewStore(config.OutDir, config.RunID)
	if err != nil {
		return nil, err
	}
	client := config.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	server := &Server{
		config:   config,
		upstream: upstream,
		store:    store,
		client:   client,
	}
	server.http = &http.Server{
		Addr:              config.Addr,
		Handler:           server,
		ReadHeaderTimeout: 30 * time.Second,
	}
	return server, nil
}

func (s *Server) ListenAndServe() error {
	return s.http.ListenAndServe()
}

func (s *Server) Serve(listener net.Listener) error {
	return s.http.Serve(listener)
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
		return
	}
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := s.handleProxy(w, r); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
	}
}

func (s *Server) authorized(r *http.Request) bool {
	if s.config.ProxyAPIKey == "" {
		return true
	}
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") && strings.TrimPrefix(auth, "Bearer ") == s.config.ProxyAPIKey {
		return true
	}
	return r.Header.Get("X-Api-Key") == s.config.ProxyAPIKey
}

// handleProxy forwards one request to the upstream router and records a metrics
// artifact for it without perturbing the measured traffic. The flow is: read the
// client request, build the upstream request (auth-injected, headers cleaned),
// round-trip it, forward the status+headers and then the body to the client, and
// tee a copy of the response off the forwarding path for out-of-band parsing.
func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request) error {
	startedAt := time.Now().UTC()

	var requestBody bytes.Buffer
	if r.Body != nil {
		defer r.Body.Close()
		if _, err := io.Copy(&requestBody, r.Body); err != nil {
			return err
		}
	}

	upstreamReq, err := s.buildUpstreamRequest(r, requestBody.Bytes())
	if err != nil {
		return err
	}

	// Stamp the send time at the upstream boundary, immediately before the round
	// trip, so TTFB/TTFT/E2E exclude the client->proxy hop and the proxy's own work.
	requestSent := time.Now()
	resp, err := s.client.Do(upstreamReq)
	if err != nil {
		s.recordFailedRequest(r, startedAt, requestSent, requestBody.Len(), classifyTransportError(err), err.Error())
		return err
	}
	defer resp.Body.Close()

	// Forward the upstream status and headers to the client before any body byte.
	writeResponseHead(w, resp)

	requestID, generationID, routerFields := s.config.Router.CaptureIDs(resp.Header)
	rawPath := s.store.RawCapturePath(safeName(captureName(requestID, generationID)))

	// Tee the response to the client and an out-of-band capture, timestamping at
	// byte receipt. Forwarding is never blocked by capture or parsing.
	outcome := s.forwardAndCapture(w, resp.Body, requestSent, rawPath)

	// Parse the copied bytes off the forwarding path — never the live stream.
	parsed := parseCaptured(resp.Header, r.URL.Path, outcome)
	outcome.timing.TTFTMillis = streamTTFTMillis(outcome, resp.Header, requestSent)

	if outcome.streamErr != nil {
		s.recordStreamError(r, startedAt, requestSent, outcome.timing, requestBody.Len(), outcome.responseBytes, resp.StatusCode, resp.Header, requestID, generationID, routerFields, rawPath, parsed, outcome.streamErr)
		return nil
	}
	return s.recordSuccess(r, startedAt, requestBody.Len(), resp, outcome, parsed, requestID, generationID, routerFields, rawPath)
}

// buildUpstreamRequest clones the inbound request toward the configured upstream.
// It copies the client headers, strips hop-by-hop headers that are scoped to the
// client<->proxy connection and must not be relayed, and lets the router adapter
// inject the real upstream credential (replacing the throwaway downstream key).
// The body is forwarded verbatim — the proxy never translates API formats.
func (s *Server) buildUpstreamRequest(r *http.Request, body []byte) (*http.Request, error) {
	upstreamReq, err := http.NewRequestWithContext(r.Context(), r.Method, s.upstreamURL(r), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	copyHeaders(upstreamReq.Header, r.Header)
	cleanHopByHopHeaders(upstreamReq.Header)
	if err := s.config.Router.InjectAuth(upstreamReq); err != nil {
		return nil, err
	}
	return upstreamReq, nil
}

// writeResponseHead relays the upstream status line and headers to the client,
// stripping hop-by-hop headers that are scoped to the proxy<->upstream connection.
func writeResponseHead(w http.ResponseWriter, resp *http.Response) {
	cleanHopByHopHeaders(resp.Header)
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
}

// captureName picks a filename stem for the raw capture, preferring a router id
// and falling back to a timestamp when none is exposed on the response headers.
func captureName(requestID, generationID string) string {
	if name := firstNonEmpty(requestID, generationID); name != "" {
		return name
	}
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// captureOutcome holds everything measured while forwarding one response.
type captureOutcome struct {
	timing         artifacts.RequestTiming
	responseBytes  int
	body           []byte // copied response bytes, for out-of-band parsing
	timeline       []chunkReceipt
	captureErr     string // raw-capture goroutine error, if any
	captureDropped bool   // chunks dropped under backpressure to preserve forwarding
	streamErr      error  // non-nil if the stream ended on a read or client-write error
}

// chunkReceipt maps a byte offset in the response to when those bytes arrived, so
// the receipt time of any later-parsed event (e.g. the first token) can be recovered
// out of band without timestamping on the forwarding path.
type chunkReceipt struct {
	endOffset  int
	atUnixNano int64
}

// timeAtOffset returns the receipt time of the chunk that delivered the given byte.
func (o captureOutcome) timeAtOffset(offset int) (int64, bool) {
	for _, mark := range o.timeline {
		if mark.endOffset > offset {
			return mark.atUnixNano, true
		}
	}
	return 0, false
}

// forwardAndCapture streams the upstream response to the client while teeing a
// copy to an on-disk capture and recording timing at byte receipt. The capture
// goroutine is always closed and awaited via the defer, so it cannot leak even if
// the stream ends early or the request context is canceled (a cancel surfaces as a
// read error, which still falls through to the defer). Forwarding is never blocked
// by capture: chunks are offered to the capture channel non-blocking and dropped
// (flagged) only from the on-disk capture if the disk writer falls behind.
func (s *Server) forwardAndCapture(w http.ResponseWriter, body io.Reader, requestSent time.Time, rawPath string) (out captureOutcome) {
	chunks := make(chan []byte, 256)
	captureDone := make(chan captureResult, 1)
	go captureRaw(rawPath, chunks, captureDone)

	var responseCopy bytes.Buffer
	out.timing.RequestSentUnixNano = requestSent.UnixNano()

	defer func() {
		close(chunks)
		capture := <-captureDone
		out.captureErr = capture.Err
		out.body = responseCopy.Bytes()
		if out.timing.LastByteUnixNano != 0 {
			out.timing.E2EMillis = millis(time.Unix(0, out.timing.LastByteUnixNano).Sub(requestSent))
		}
	}()

	buf := make([]byte, 32*1024)
	for {
		n, readErr := body.Read(buf)
		receipt := time.Now()
		if n > 0 {
			if out.timing.FirstByteUnixNano == 0 {
				out.timing.FirstByteUnixNano = receipt.UnixNano()
				out.timing.TTFBMillis = millis(receipt.Sub(requestSent))
			}
			out.timing.LastByteUnixNano = receipt.UnixNano()
			out.responseBytes += n
			out.timeline = append(out.timeline, chunkReceipt{endOffset: out.responseBytes, atUnixNano: receipt.UnixNano()})

			chunk := buf[:n]
			if _, writeErr := w.Write(chunk); writeErr != nil {
				teeChunk(&responseCopy, chunks, chunk, &out.captureDropped)
				out.streamErr = writeErr
				return out
			}
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			teeChunk(&responseCopy, chunks, chunk, &out.captureDropped)
		}
		if readErr == io.EOF {
			return out
		}
		if readErr != nil {
			out.streamErr = readErr
			return out
		}
	}
}

// teeChunk copies a forwarded chunk into the in-memory parse buffer and offers it
// to the raw-capture channel without blocking. If the capture can't keep up the
// chunk is dropped from the on-disk capture only (never from the client stream or
// the parse buffer), and the drop is flagged.
func teeChunk(buf *bytes.Buffer, chunks chan<- []byte, chunk []byte, dropped *bool) {
	c := append([]byte(nil), chunk...)
	buf.Write(c)
	select {
	case chunks <- c:
	default:
		*dropped = true
	}
}

// parseCaptured parses the copied response bytes out of band and folds in decode
// and capture warnings. It never touches the live response stream.
func parseCaptured(header http.Header, endpoint string, out captureOutcome) parsers.Result {
	parseBody, decodeWarnings := decodeCopiedBody(header.Get("Content-Encoding"), out.body)
	parsed := parsers.Parse(endpoint, header.Get("Content-Type"), parseBody)
	parsed.Warnings = append(decodeWarnings, parsed.Warnings...)
	if out.captureErr != "" {
		parsed.Warnings = append(parsed.Warnings, "raw capture failed: "+out.captureErr)
	}
	if out.captureDropped {
		parsed.Warnings = append(parsed.Warnings, "raw capture dropped chunks to preserve forwarding")
	}
	return parsed
}

// recordSuccess builds and appends the request record for a fully forwarded
// response.
func (s *Server) recordSuccess(r *http.Request, startedAt time.Time, requestBytes int, resp *http.Response, outcome captureOutcome, parsed parsers.Result, requestID, generationID string, routerFields map[string]any, rawPath string) error {
	record := artifacts.RequestRecord{
		SchemaVersion: schemaVersion,
		RunID:         s.config.RunID,
		RequestID:     firstNonEmpty(parsed.ID, requestID),
		GenerationID:  firstNonEmpty(parsed.GenerationID, generationID),
		Router:        s.config.Router.Name(),
		Method:        r.Method,
		Endpoint:      r.URL.Path,
		StatusCode:    resp.StatusCode,
		Success:       resp.StatusCode >= 200 && resp.StatusCode < 400,
		ErrorClass:    classifyStatus(resp.StatusCode),
		StartedAt:     startedAt,
		Timing:        outcome.timing,
		Usage:         parsed.Usage,
		Comparable: artifacts.ComparableMetrics{
			CostUSD:              parsed.Usage.CostUSD,
			TTFBMillis:           outcome.timing.TTFBMillis,
			TTFTMillis:           outcome.timing.TTFTMillis,
			E2EMillis:            outcome.timing.E2EMillis,
			OutputTokensPerSec:   outputTokensPerSec(parsed.Usage.OutputTokens, outcome.timing, resp.Header.Get("Content-Type")),
			ToolCallValidityRate: toolCallValidityRate(parsed.ToolCalls),
		},
		Context: artifacts.ContextMetrics{
			InputTokens:        parsed.Usage.InputTokens,
			OutputTokens:       parsed.Usage.OutputTokens,
			RequestBytes:       requestBytes,
			ResponseBytes:      outcome.responseBytes,
			ToolCallCount:      parsed.ToolCalls.Count,
			ValidToolCallCount: parsed.ToolCalls.ValidCount,
		},
		Diagnostics: artifacts.Diagnostics{
			Headers:        sanitizeHeaders(resp.Header),
			ParserWarnings: parsed.Warnings,
			RawCapturePath: rawPath,
			RouterSpecific: routerFields,
			ToolCalls:      parsed.ToolDetails,
		},
	}
	if record.Usage.CostState == "" {
		record.Usage.CostState = artifacts.CostStatePending
	}
	return s.store.AppendRequest(record)
}

func (s *Server) upstreamURL(r *http.Request) string {
	u := *s.upstream
	basePath := strings.TrimRight(u.Path, "/")
	reqPath := "/" + strings.TrimLeft(r.URL.Path, "/")
	u.Path = path.Join(basePath, reqPath)
	if strings.HasSuffix(r.URL.Path, "/") && !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	u.RawQuery = r.URL.RawQuery
	return u.String()
}

func (s *Server) recordFailedRequest(r *http.Request, startedAt time.Time, requestSent time.Time, requestBytes int, errorClass string, errText string) {
	_ = s.store.AppendRequest(artifacts.RequestRecord{
		SchemaVersion: schemaVersion,
		RunID:         s.config.RunID,
		Router:        s.config.Router.Name(),
		Method:        r.Method,
		Endpoint:      r.URL.Path,
		StatusCode:    0,
		Success:       false,
		ErrorClass:    errorClass,
		Error:         errText,
		StartedAt:     startedAt,
		Timing: artifacts.RequestTiming{
			RequestSentUnixNano: requestSent.UnixNano(),
		},
		Usage: artifacts.Usage{CostState: artifacts.CostStateUnavailable},
		Context: artifacts.ContextMetrics{
			RequestBytes: requestBytes,
		},
	})
}

func (s *Server) recordStreamError(r *http.Request, startedAt time.Time, requestSent time.Time, timing artifacts.RequestTiming, requestBytes int, responseBytes int, statusCode int, headers http.Header, requestID string, generationID string, routerFields map[string]any, rawPath string, parsed parsers.Result, err error) {
	success := statusCode >= 200 && statusCode < 400
	errorClass := "stream"
	if success && responseBytes > 0 && isClientCancel(err) {
		errorClass = "client_cancel"
	}
	usage := parsed.Usage
	if usage.CostState == "" {
		usage.CostState = artifacts.CostStatePending
	}
	_ = s.store.AppendRequest(artifacts.RequestRecord{
		SchemaVersion: schemaVersion,
		RunID:         s.config.RunID,
		RequestID:     firstNonEmpty(parsed.ID, requestID),
		GenerationID:  firstNonEmpty(parsed.GenerationID, generationID),
		Router:        s.config.Router.Name(),
		Method:        r.Method,
		Endpoint:      r.URL.Path,
		StatusCode:    statusCode,
		Success:       success,
		ErrorClass:    errorClass,
		Error:         err.Error(),
		StartedAt:     startedAt,
		Timing:        timing,
		Usage:         usage,
		Comparable: artifacts.ComparableMetrics{
			CostUSD:              usage.CostUSD,
			TTFBMillis:           timing.TTFBMillis,
			TTFTMillis:           timing.TTFTMillis,
			E2EMillis:            timing.E2EMillis,
			OutputTokensPerSec:   outputTokensPerSec(usage.OutputTokens, timing, headers.Get("Content-Type")),
			ToolCallValidityRate: toolCallValidityRate(parsed.ToolCalls),
		},
		Context: artifacts.ContextMetrics{
			InputTokens:        usage.InputTokens,
			OutputTokens:       usage.OutputTokens,
			RequestBytes:       requestBytes,
			ResponseBytes:      responseBytes,
			ToolCallCount:      parsed.ToolCalls.Count,
			ValidToolCallCount: parsed.ToolCalls.ValidCount,
		},
		Diagnostics: artifacts.Diagnostics{
			Headers:        sanitizeHeaders(headers),
			ParserWarnings: parsed.Warnings,
			RawCapturePath: rawPath,
			RouterSpecific: routerFields,
			ToolCalls:      parsed.ToolDetails,
		},
	})
}

func isClientCancel(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "context canceled") ||
		strings.Contains(text, "broken pipe") ||
		strings.Contains(text, "connection reset by peer")
}

type captureResult struct {
	Err string
}

func captureRaw(path string, chunks <-chan []byte, done chan<- captureResult) {
	if err := os.MkdirAll(pathDir(path), 0o755); err != nil {
		done <- captureResult{Err: err.Error()}
		return
	}
	f, err := os.Create(path)
	if err != nil {
		done <- captureResult{Err: err.Error()}
		return
	}
	defer f.Close()
	for chunk := range chunks {
		if _, err := f.Write(chunk); err != nil {
			done <- captureResult{Err: err.Error()}
			return
		}
	}
	done <- captureResult{}
}

func copyHeaders(dst, src http.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func cleanHopByHopHeaders(headers http.Header) {
	for _, key := range []string{
		"Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Te",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
	} {
		headers.Del(key)
	}
}

func sanitizeHeaders(headers http.Header) map[string][]string {
	out := map[string][]string{}
	for key, values := range headers {
		lower := strings.ToLower(key)
		if lower == "authorization" || lower == "cookie" || lower == "set-cookie" {
			continue
		}
		out[key] = append([]string(nil), values...)
	}
	return out
}

func classifyStatus(status int) string {
	switch {
	case status == 0:
		return "transport"
	case status >= 200 && status < 400:
		return ""
	case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
		return "timeout"
	case status >= 400 && status < 500:
		return "4xx"
	case status >= 500:
		return "5xx"
	default:
		return "http"
	}
}

func classifyTransportError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "transport"
}

// streamTTFTMillis is time to first token: the receipt time of the first streamed
// content delta minus request-sent. It is 0 for non-streaming or compressed bodies
// (whose offsets would not map to the wire) and when no token delta is present.
func streamTTFTMillis(out captureOutcome, header http.Header, requestSent time.Time) float64 {
	if !strings.Contains(strings.ToLower(header.Get("Content-Type")), "text/event-stream") {
		return 0
	}
	if header.Get("Content-Encoding") != "" {
		return 0
	}
	offset, ok := firstStreamDeltaOffset(out.body)
	if !ok {
		return 0
	}
	at, ok := out.timeAtOffset(offset)
	if !ok {
		return 0
	}
	return millis(time.Unix(0, at).Sub(requestSent))
}

// firstStreamDeltaOffset returns the byte offset of the first SSE data line carrying a
// token-bearing delta: any "*.delta" event with a non-empty string "delta" (output
// text, reasoning, or tool-call argument tokens).
func firstStreamDeltaOffset(body []byte) (int, bool) {
	offset := 0
	for len(body) > 0 {
		nl := bytes.IndexByte(body, '\n')
		line := body
		if nl >= 0 {
			line = body[:nl]
		}
		if data := bytes.TrimSpace(line); bytes.HasPrefix(data, []byte("data:")) {
			if isStreamTokenDelta(bytes.TrimSpace(data[len("data:"):])) {
				return offset, true
			}
		}
		if nl < 0 {
			break
		}
		offset += nl + 1
		body = body[nl+1:]
	}
	return 0, false
}

func isStreamTokenDelta(data []byte) bool {
	var ev struct {
		Type  string          `json:"type"`
		Delta json.RawMessage `json:"delta"`
	}
	if err := json.Unmarshal(data, &ev); err != nil {
		return false
	}
	if !strings.HasSuffix(ev.Type, ".delta") {
		return false
	}
	var text string
	return json.Unmarshal(ev.Delta, &text) == nil && text != ""
}

func outputTokensPerSec(tokens int, timing artifacts.RequestTiming, contentType string) float64 {
	if !strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		return 0
	}
	if tokens <= 0 || timing.FirstByteUnixNano == 0 || timing.LastByteUnixNano == 0 {
		return 0
	}
	d := time.Unix(0, timing.LastByteUnixNano).Sub(time.Unix(0, timing.FirstByteUnixNano))
	if d <= 0 {
		return 0
	}
	return float64(tokens) / d.Seconds()
}

func toolCallValidityRate(metrics artifacts.ToolCallMetrics) float64 {
	if metrics.Count == 0 {
		return 0
	}
	return float64(metrics.ValidCount) / float64(metrics.Count)
}

func millis(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000.0
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func safeName(value string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_")
	return replacer.Replace(value)
}

func pathDir(p string) string {
	if i := strings.LastIndex(p, string(os.PathSeparator)); i >= 0 {
		return p[:i]
	}
	return "."
}

func decodeCopiedBody(contentEncoding string, body []byte) ([]byte, []string) {
	switch strings.ToLower(strings.TrimSpace(contentEncoding)) {
	case "", "identity":
		return body, nil
	case "gzip":
		reader, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return body, []string{"gzip decode failed: " + err.Error()}
		}
		defer reader.Close()
		decoded, err := io.ReadAll(reader)
		if err != nil {
			return body, []string{"gzip decode failed: " + err.Error()}
		}
		return decoded, nil
	default:
		return body, []string{"unsupported content encoding for parser: " + contentEncoding}
	}
}
