package apicompat

import (
	"encoding/json"
	"strings"
	"time"
)

// AnthropicToChatCompletions converts a Messages API response into a Chat
// Completions response.
func AnthropicToChatCompletions(resp *AnthropicResponse, model string) *ChatCompletionsResponse {
	if resp == nil {
		return &ChatCompletionsResponse{
			ID:      generateChatCmplID(),
			Object:  "chat.completion",
			Created: time.Now().Unix(),
			Model:   model,
			Choices: []ChatChoice{{
				Index:        0,
				Message:      ChatMessage{Role: "assistant"},
				FinishReason: "stop",
			}},
		}
	}

	responseModel := model
	if strings.TrimSpace(responseModel) == "" {
		responseModel = resp.Model
	}

	msg := ChatMessage{Role: "assistant"}
	toolCalls := make([]ChatToolCall, 0)
	var textBuilder strings.Builder
	var reasoningBuilder strings.Builder

	for idx, block := range resp.Content {
		switch block.Type {
		case "text":
			textBuilder.WriteString(block.Text)
		case "thinking", "redacted_thinking":
			reasoningBuilder.WriteString(block.Thinking)
		case "tool_use":
			toolCalls = append(toolCalls, ChatToolCall{
				Index: intPointer(idx),
				ID:    strings.TrimSpace(block.ID),
				Type:  "function",
				Function: ChatFunctionCall{
					Name:      strings.TrimSpace(block.Name),
					Arguments: normalizeAnthropicToolInput(block.Input),
				},
			})
		}
	}

	if textBuilder.Len() > 0 {
		contentJSON, _ := json.Marshal(textBuilder.String())
		msg.Content = contentJSON
	}
	if reasoningBuilder.Len() > 0 {
		msg.ReasoningContent = reasoningBuilder.String()
	}
	if len(toolCalls) > 0 {
		msg.ToolCalls = toolCalls
	}

	id := strings.TrimSpace(resp.ID)
	if id == "" {
		id = generateChatCmplID()
	}

	out := &ChatCompletionsResponse{
		ID:      id,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   responseModel,
		Choices: []ChatChoice{{
			Index:        0,
			Message:      msg,
			FinishReason: anthropicStopReasonToChatFinishReason(resp.StopReason, len(toolCalls) > 0),
		}},
	}
	out.Usage = anthropicUsageToChatUsage(resp.Usage)
	return out
}

// AnthropicToChatState tracks state while converting Anthropic SSE events to
// Chat Completions SSE chunks.
type AnthropicToChatState struct {
	ID                    string
	Model                 string
	Created               int64
	SentRole              bool
	SawToolCall           bool
	Finalized             bool
	IncludeUsage          bool
	PendingStopReason     string
	Usage                 *ChatUsage
	NextToolCallIndex     int
	BlockIndexToToolIndex map[int]int
}

// NewAnthropicToChatState returns an initialized stream state.
func NewAnthropicToChatState(includeUsage bool) *AnthropicToChatState {
	return &AnthropicToChatState{
		ID:                    generateChatCmplID(),
		Created:               time.Now().Unix(),
		IncludeUsage:          includeUsage,
		BlockIndexToToolIndex: make(map[int]int),
	}
}

// AnthropicEventToChatChunks converts a single Anthropic SSE event into zero or
// more Chat Completions chunks.
func AnthropicEventToChatChunks(eventName string, evt *AnthropicStreamEvent, state *AnthropicToChatState) []ChatCompletionsChunk {
	if evt == nil || state == nil {
		return nil
	}

	eventType := strings.TrimSpace(evt.Type)
	if eventType == "" {
		eventType = strings.TrimSpace(eventName)
	}

	switch eventType {
	case "message_start":
		return anthropicMessageStartToChat(evt, state)
	case "content_block_start":
		return anthropicContentBlockStartToChat(evt, state)
	case "content_block_delta":
		return anthropicContentBlockDeltaToChat(evt, state)
	case "message_delta":
		anthropicMessageDeltaToChat(evt, state)
		return nil
	case "message_stop":
		return finalizeAnthropicChatStreamWithReason(state)
	default:
		return nil
	}
}

// FinalizeAnthropicChatStream emits a synthetic terminal chunk when the
// upstream stream ended unexpectedly without `message_stop`.
func FinalizeAnthropicChatStream(state *AnthropicToChatState) []ChatCompletionsChunk {
	if state == nil || state.Finalized {
		return nil
	}
	return finalizeAnthropicChatStreamWithReason(state)
}

func anthropicMessageStartToChat(evt *AnthropicStreamEvent, state *AnthropicToChatState) []ChatCompletionsChunk {
	if evt.Message != nil {
		if strings.TrimSpace(evt.Message.ID) != "" {
			state.ID = evt.Message.ID
		}
		if strings.TrimSpace(evt.Message.Model) != "" {
			state.Model = evt.Message.Model
		}
	}
	if state.SentRole {
		return nil
	}
	state.SentRole = true
	return []ChatCompletionsChunk{makeChatDeltaChunk(&ResponsesEventToChatState{
		ID:      state.ID,
		Model:   state.Model,
		Created: state.Created,
	}, ChatDelta{Role: "assistant"})}
}

func anthropicContentBlockStartToChat(evt *AnthropicStreamEvent, state *AnthropicToChatState) []ChatCompletionsChunk {
	if evt.ContentBlock == nil || evt.Index == nil {
		return nil
	}
	if evt.ContentBlock.Type != "tool_use" {
		return nil
	}

	idx := state.NextToolCallIndex
	state.NextToolCallIndex++
	state.BlockIndexToToolIndex[*evt.Index] = idx
	state.SawToolCall = true

	return []ChatCompletionsChunk{makeChatDeltaChunk(&ResponsesEventToChatState{
		ID:      state.ID,
		Model:   state.Model,
		Created: state.Created,
	}, ChatDelta{
		ToolCalls: []ChatToolCall{{
			Index: &idx,
			ID:    strings.TrimSpace(evt.ContentBlock.ID),
			Type:  "function",
			Function: ChatFunctionCall{
				Name: strings.TrimSpace(evt.ContentBlock.Name),
			},
		}},
	})}
}

func anthropicContentBlockDeltaToChat(evt *AnthropicStreamEvent, state *AnthropicToChatState) []ChatCompletionsChunk {
	if evt.Delta == nil {
		return nil
	}

	switch evt.Delta.Type {
	case "text_delta":
		text := evt.Delta.Text
		if text == "" {
			return nil
		}
		return []ChatCompletionsChunk{makeChatDeltaChunk(&ResponsesEventToChatState{
			ID:      state.ID,
			Model:   state.Model,
			Created: state.Created,
		}, ChatDelta{Content: &text})}
	case "thinking_delta":
		reasoning := evt.Delta.Thinking
		if reasoning == "" {
			return nil
		}
		return []ChatCompletionsChunk{makeChatDeltaChunk(&ResponsesEventToChatState{
			ID:      state.ID,
			Model:   state.Model,
			Created: state.Created,
		}, ChatDelta{ReasoningContent: &reasoning})}
	case "input_json_delta":
		if evt.Index == nil {
			return nil
		}
		toolCallIndex, ok := state.BlockIndexToToolIndex[*evt.Index]
		if !ok {
			return nil
		}
		args := evt.Delta.PartialJSON
		if args == "" {
			return nil
		}
		return []ChatCompletionsChunk{makeChatDeltaChunk(&ResponsesEventToChatState{
			ID:      state.ID,
			Model:   state.Model,
			Created: state.Created,
		}, ChatDelta{
			ToolCalls: []ChatToolCall{{
				Index: &toolCallIndex,
				Function: ChatFunctionCall{
					Arguments: args,
				},
			}},
		})}
	default:
		return nil
	}
}

func anthropicMessageDeltaToChat(evt *AnthropicStreamEvent, state *AnthropicToChatState) {
	if evt.Delta != nil && strings.TrimSpace(evt.Delta.StopReason) != "" {
		state.PendingStopReason = strings.TrimSpace(evt.Delta.StopReason)
	}
	if evt.Usage != nil {
		state.Usage = anthropicUsageToChatUsage(*evt.Usage)
	}
}

func finalizeAnthropicChatStreamWithReason(state *AnthropicToChatState) []ChatCompletionsChunk {
	if state == nil || state.Finalized {
		return nil
	}
	state.Finalized = true

	finishReason := anthropicStopReasonToChatFinishReason(state.PendingStopReason, state.SawToolCall)
	chatState := &ResponsesEventToChatState{
		ID:      state.ID,
		Model:   state.Model,
		Created: state.Created,
		Usage:   state.Usage,
	}

	chunks := []ChatCompletionsChunk{makeChatFinishChunk(chatState, finishReason)}
	if state.IncludeUsage && state.Usage != nil {
		chunks = append(chunks, ChatCompletionsChunk{
			ID:      state.ID,
			Object:  "chat.completion.chunk",
			Created: state.Created,
			Model:   state.Model,
			Choices: []ChatChunkChoice{},
			Usage:   state.Usage,
		})
	}
	return chunks
}

func anthropicUsageToChatUsage(usage AnthropicUsage) *ChatUsage {
	promptTokens := usage.InputTokens + usage.CacheCreationInputTokens + usage.CacheReadInputTokens
	totalTokens := promptTokens + usage.OutputTokens
	out := &ChatUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: usage.OutputTokens,
		TotalTokens:      totalTokens,
	}
	if usage.CacheReadInputTokens > 0 {
		out.PromptTokensDetails = &ChatTokenDetails{CachedTokens: usage.CacheReadInputTokens}
	}
	return out
}

func anthropicStopReasonToChatFinishReason(stopReason string, sawToolCall bool) string {
	switch strings.TrimSpace(stopReason) {
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	default:
		if sawToolCall {
			return "tool_calls"
		}
		return "stop"
	}
}

func normalizeAnthropicToolInput(raw json.RawMessage) string {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return "{}"
	}
	if !json.Valid(raw) {
		quoted, _ := json.Marshal(string(raw))
		return string(quoted)
	}
	return string(raw)
}

func intPointer(v int) *int {
	value := v
	return &value
}
