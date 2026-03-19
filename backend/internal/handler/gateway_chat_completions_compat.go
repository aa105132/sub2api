package handler

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// ChatCompletions 为非 OpenAI 分组提供 `/v1/chat/completions` 兼容层。
// 实现策略：请求先转成 Anthropic Messages，再完整复用通用 `Messages`
// 调度/缓存/计费链路，最后把响应再转回 Chat Completions。
func (h *GatewayHandler) ChatCompletions(c *gin.Context) {
	body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		if maxErr, ok := extractMaxBytesError(err); ok {
			writeGatewayChatError(c, http.StatusRequestEntityTooLarge, "invalid_request_error", buildBodyTooLargeMessage(maxErr.Limit))
			return
		}
		writeGatewayChatError(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}
	if len(body) == 0 {
		writeGatewayChatError(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		return
	}

	var chatReq apicompat.ChatCompletionsRequest
	if err := json.Unmarshal(body, &chatReq); err != nil {
		writeGatewayChatError(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return
	}

	anthropicReq, err := apicompat.ChatCompletionsToAnthropic(&chatReq)
	if err != nil {
		writeGatewayChatError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if sessionSeed := extractGatewayChatCompatSessionSeed(c, body); sessionSeed != "" {
		apicompat.InjectAnthropicCompatSessionMetadata(anthropicReq, sessionSeed)
	}

	anthropicBody, err := json.Marshal(anthropicReq)
	if err != nil {
		writeGatewayChatError(c, http.StatusInternalServerError, "api_error", "Failed to translate request body")
		return
	}

	compatCtx := c.Copy()
	compatCtx.Request = c.Request.Clone(c.Request.Context())
	compatCtx.Request.Header = compatCtx.Request.Header.Clone()
	compatCtx.Request.Header.Set("Content-Type", "application/json")
	compatCtx.Request.ContentLength = int64(len(anthropicBody))
	compatCtx.Request.Body = ioNopCloserBytes(anthropicBody)

	if !chatReq.Stream {
		bufferedWriter := newGatewayBufferedWriter(c.Writer)
		compatCtx.Writer = bufferedWriter
		h.Messages(compatCtx)
		writeBufferedAnthropicAsChat(c.Writer, bufferedWriter, chatReq.Model)
		return
	}

	streamWriter := newGatewayAnthropicChatStreamWriter(c.Writer, chatReq.Model, chatReq.StreamOptions != nil && chatReq.StreamOptions.IncludeUsage)
	compatCtx.Writer = streamWriter
	h.Messages(compatCtx)
	if err := streamWriter.Finalize(); err != nil {
		writeGatewayChatError(c, http.StatusBadGateway, "upstream_error", err.Error())
	}
}

type gatewayBufferedWriter struct {
	original gin.ResponseWriter
	header   http.Header
	body     bytes.Buffer
	status   int
	size     int
}

func newGatewayBufferedWriter(original gin.ResponseWriter) *gatewayBufferedWriter {
	return &gatewayBufferedWriter{
		original: original,
		header:   make(http.Header),
	}
}

func (w *gatewayBufferedWriter) Header() http.Header { return w.header }

func (w *gatewayBufferedWriter) WriteHeader(statusCode int) {
	if w.status == 0 {
		w.status = statusCode
	}
}

func (w *gatewayBufferedWriter) WriteHeaderNow() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
}

func (w *gatewayBufferedWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.body.Write(data)
	w.size += n
	return n, err
}

func (w *gatewayBufferedWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }

func (w *gatewayBufferedWriter) Status() int { return w.status }

func (w *gatewayBufferedWriter) Size() int { return w.size }

func (w *gatewayBufferedWriter) Written() bool { return w.status != 0 || w.size > 0 }

func (w *gatewayBufferedWriter) Flush() {}

func (w *gatewayBufferedWriter) CloseNotify() <-chan bool { return w.original.CloseNotify() }

func (w *gatewayBufferedWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.original.Hijack()
}

func (w *gatewayBufferedWriter) Pusher() http.Pusher { return w.original.Pusher() }

type gatewayAnthropicChatStreamWriter struct {
	original     gin.ResponseWriter
	header       http.Header
	status       int
	size         int
	headerSent   bool
	pending      strings.Builder
	bufferedBody bytes.Buffer
	model        string
	state        *apicompat.AnthropicToChatState
	doneSent     bool
}

func newGatewayAnthropicChatStreamWriter(original gin.ResponseWriter, model string, includeUsage bool) *gatewayAnthropicChatStreamWriter {
	return &gatewayAnthropicChatStreamWriter{
		original: original,
		header:   make(http.Header),
		model:    model,
		state:    apicompat.NewAnthropicToChatState(includeUsage),
	}
}

func (w *gatewayAnthropicChatStreamWriter) Header() http.Header { return w.header }

func (w *gatewayAnthropicChatStreamWriter) WriteHeader(statusCode int) {
	if w.status == 0 {
		w.status = statusCode
	}
}

func (w *gatewayAnthropicChatStreamWriter) WriteHeaderNow() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
}

func (w *gatewayAnthropicChatStreamWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.size += len(data)
	if !w.isStreamingSSE() {
		return w.bufferedBody.Write(data)
	}
	if _, err := w.pending.WriteString(string(data)); err != nil {
		return 0, err
	}
	return len(data), w.processPendingFrames(false)
}

func (w *gatewayAnthropicChatStreamWriter) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

func (w *gatewayAnthropicChatStreamWriter) Status() int { return w.status }

func (w *gatewayAnthropicChatStreamWriter) Size() int { return w.size }

func (w *gatewayAnthropicChatStreamWriter) Written() bool { return w.status != 0 || w.size > 0 }

func (w *gatewayAnthropicChatStreamWriter) Flush() {
	if w.isStreamingSSE() {
		_ = w.processPendingFrames(false)
		if w.headerSent {
			w.original.Flush()
		}
	}
}

func (w *gatewayAnthropicChatStreamWriter) CloseNotify() <-chan bool { return w.original.CloseNotify() }

func (w *gatewayAnthropicChatStreamWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.original.Hijack()
}

func (w *gatewayAnthropicChatStreamWriter) Pusher() http.Pusher { return w.original.Pusher() }

func (w *gatewayAnthropicChatStreamWriter) Finalize() error {
	if w.isStreamingSSE() {
		if err := w.processPendingFrames(true); err != nil {
			return err
		}
		for _, chunk := range apicompat.FinalizeAnthropicChatStream(w.state) {
			if err := w.writeChatChunk(chunk); err != nil {
				return err
			}
		}
		return w.writeDoneIfNeeded()
	}

	if w.status >= 400 {
		writeAnthropicErrorAsChat(w.original, w.statusOrOK(), w.header, w.bufferedBody.Bytes())
		return nil
	}
	writeGatewayChatErrorWithHeaders(w.original, http.StatusBadGateway, w.header, "upstream_error", "Expected streaming SSE response")
	return nil
}

func (w *gatewayAnthropicChatStreamWriter) isStreamingSSE() bool {
	return w.statusOrOK() < 400 && strings.Contains(strings.ToLower(w.header.Get("Content-Type")), "text/event-stream")
}

func (w *gatewayAnthropicChatStreamWriter) statusOrOK() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *gatewayAnthropicChatStreamWriter) processPendingFrames(final bool) error {
	pending := w.pending.String()
	for {
		index := strings.Index(pending, "\n\n")
		if index < 0 {
			break
		}
		frame := pending[:index]
		pending = pending[index+2:]
		if err := w.handleAnthropicFrame(frame); err != nil {
			return err
		}
	}
	if final && strings.TrimSpace(pending) != "" {
		if err := w.handleAnthropicFrame(pending); err != nil {
			return err
		}
		pending = ""
	}
	w.pending.Reset()
	if pending != "" {
		_, _ = w.pending.WriteString(pending)
	}
	return nil
}

func (w *gatewayAnthropicChatStreamWriter) handleAnthropicFrame(frame string) error {
	frame = strings.Trim(frame, "\r\n")
	if strings.TrimSpace(frame) == "" {
		return nil
	}

	var eventName string
	dataLines := make([]string, 0, 1)
	for _, rawLine := range strings.Split(frame, "\n") {
		line := strings.TrimRight(rawLine, "\r")
		switch {
		case strings.HasPrefix(line, "event:"):
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		case strings.HasPrefix(line, ":"):
			continue
		}
	}
	if strings.EqualFold(eventName, "ping") {
		return nil
	}

	payload := strings.Join(dataLines, "\n")
	if payload == "" {
		return nil
	}

	var evt apicompat.AnthropicStreamEvent
	if err := json.Unmarshal([]byte(payload), &evt); err != nil {
		return fmt.Errorf("parse anthropic stream event: %w", err)
	}
	if evt.Type == "" {
		evt.Type = eventName
	}
	chunks := apicompat.AnthropicEventToChatChunks(eventName, &evt, w.state)
	for _, chunk := range chunks {
		if err := w.writeChatChunk(chunk); err != nil {
			return err
		}
	}
	if evt.Type == "message_stop" {
		return w.writeDoneIfNeeded()
	}
	return nil
}

func (w *gatewayAnthropicChatStreamWriter) writeChatChunk(chunk apicompat.ChatCompletionsChunk) error {
	if strings.TrimSpace(chunk.Model) == "" {
		chunk.Model = w.model
	}
	w.ensureStreamHeaders()
	sse, err := apicompat.ChatChunkToSSE(chunk)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprint(w.original, sse); err != nil {
		return err
	}
	w.original.Flush()
	return nil
}

func (w *gatewayAnthropicChatStreamWriter) writeDoneIfNeeded() error {
	if w.doneSent {
		return nil
	}
	w.ensureStreamHeaders()
	w.doneSent = true
	if _, err := fmt.Fprint(w.original, "data: [DONE]\n\n"); err != nil {
		return err
	}
	w.original.Flush()
	return nil
}

func (w *gatewayAnthropicChatStreamWriter) ensureStreamHeaders() {
	if w.headerSent {
		return
	}
	copyGatewayCompatHeaders(w.original.Header(), w.header)
	w.original.Header().Set("Content-Type", "text/event-stream")
	w.original.Header().Del("Content-Length")
	w.original.WriteHeader(w.statusOrOK())
	w.headerSent = true
}

func writeBufferedAnthropicAsChat(original gin.ResponseWriter, buffered *gatewayBufferedWriter, model string) {
	statusCode := buffered.status
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	if statusCode >= 400 {
		writeAnthropicErrorAsChat(original, statusCode, buffered.header, buffered.body.Bytes())
		return
	}

	var anthropicResp apicompat.AnthropicResponse
	if err := json.Unmarshal(buffered.body.Bytes(), &anthropicResp); err != nil {
		writeGatewayChatErrorWithHeaders(original, http.StatusBadGateway, buffered.header, "upstream_error", "Failed to translate upstream response")
		return
	}

	chatResp := apicompat.AnthropicToChatCompletions(&anthropicResp, model)
	payload, err := json.Marshal(chatResp)
	if err != nil {
		writeGatewayChatErrorWithHeaders(original, http.StatusBadGateway, buffered.header, "upstream_error", "Failed to serialize translated response")
		return
	}

	copyGatewayCompatHeaders(original.Header(), buffered.header)
	original.Header().Set("Content-Type", "application/json; charset=utf-8")
	original.Header().Del("Content-Length")
	original.WriteHeader(statusCode)
	_, _ = original.Write(payload)
}

func writeAnthropicErrorAsChat(original gin.ResponseWriter, statusCode int, headers http.Header, body []byte) {
	errType := strings.TrimSpace(gjson.GetBytes(body, "error.type").String())
	if errType == "" {
		errType = "api_error"
	}
	message := strings.TrimSpace(gjson.GetBytes(body, "error.message").String())
	if message == "" {
		message = strings.TrimSpace(gjson.GetBytes(body, "message").String())
	}
	if message == "" {
		message = "Upstream request failed"
	}
	writeGatewayChatErrorWithHeaders(original, statusCode, headers, errType, message)
}

func writeGatewayChatError(c *gin.Context, statusCode int, errType, message string) {
	c.JSON(statusCode, gin.H{
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
}

func writeGatewayChatErrorWithHeaders(original gin.ResponseWriter, statusCode int, headers http.Header, errType, message string) {
	copyGatewayCompatHeaders(original.Header(), headers)
	original.Header().Set("Content-Type", "application/json; charset=utf-8")
	original.Header().Del("Content-Length")
	original.WriteHeader(statusCode)
	payload, _ := json.Marshal(gin.H{
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
	_, _ = original.Write(payload)
}

func copyGatewayCompatHeaders(dst, src http.Header) {
	for key := range dst {
		dst.Del(key)
	}
	for key, values := range src {
		if strings.EqualFold(key, "Content-Length") {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func extractGatewayChatCompatSessionSeed(c *gin.Context, body []byte) string {
	if c != nil {
		if sessionID := strings.TrimSpace(c.GetHeader("session_id")); sessionID != "" {
			return sessionID
		}
		if conversationID := strings.TrimSpace(c.GetHeader("conversation_id")); conversationID != "" {
			return conversationID
		}
	}
	if len(body) == 0 {
		return ""
	}
	if promptCacheKey := strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String()); promptCacheKey != "" {
		return promptCacheKey
	}
	return strings.TrimSpace(gjson.GetBytes(body, "metadata.user_id").String())
}

type readCloserBuffer struct {
	*bytes.Reader
}

func ioNopCloserBytes(body []byte) *readCloserBuffer {
	return &readCloserBuffer{Reader: bytes.NewReader(body)}
}

func (r *readCloserBuffer) Close() error { return nil }
