package apicompat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// ChatCompletionsToAnthropic converts a Chat Completions request into an
// Anthropic Messages request so non-OpenAI providers can reuse the generic
// `/v1/messages` gateway chain.
func ChatCompletionsToAnthropic(req *ChatCompletionsRequest) (*AnthropicRequest, error) {
	if req == nil {
		return nil, fmt.Errorf("nil chat completions request")
	}

	out := &AnthropicRequest{
		Model:       req.Model,
		Stream:      req.Stream,
		Temperature: req.Temperature,
		TopP:        req.TopP,
	}

	if req.MaxCompletionTokens != nil && *req.MaxCompletionTokens > 0 {
		out.MaxTokens = *req.MaxCompletionTokens
	} else if req.MaxTokens != nil && *req.MaxTokens > 0 {
		out.MaxTokens = *req.MaxTokens
	}
	if out.MaxTokens <= 0 {
		out.MaxTokens = minMaxOutputTokens
	}

	systemBlocks := make([]AnthropicContentBlock, 0)
	out.Messages = make([]AnthropicMessage, 0, len(req.Messages))

	for _, msg := range req.Messages {
		switch strings.ToLower(strings.TrimSpace(msg.Role)) {
		case "system":
			text, err := parseChatContent(msg.Content)
			if err != nil {
				return nil, fmt.Errorf("convert system message: %w", err)
			}
			if strings.TrimSpace(text) != "" {
				systemBlocks = append(systemBlocks, AnthropicContentBlock{
					Type: "text",
					Text: text,
				})
			}
		case "assistant":
			anthropicMsg, ok, err := chatAssistantMessageToAnthropic(msg)
			if err != nil {
				return nil, err
			}
			if ok {
				out.Messages = append(out.Messages, anthropicMsg)
			}
		case "tool":
			anthropicMsg, ok, err := chatToolMessageToAnthropic(msg)
			if err != nil {
				return nil, err
			}
			if ok {
				out.Messages = append(out.Messages, anthropicMsg)
			}
		case "function":
			anthropicMsg, ok, err := chatFunctionMessageToAnthropic(msg)
			if err != nil {
				return nil, err
			}
			if ok {
				out.Messages = append(out.Messages, anthropicMsg)
			}
		default:
			anthropicMsg, ok, err := chatUserMessageToAnthropic(msg)
			if err != nil {
				return nil, err
			}
			if ok {
				out.Messages = append(out.Messages, anthropicMsg)
			}
		}
	}

	if len(systemBlocks) > 0 {
		systemJSON, err := json.Marshal(systemBlocks)
		if err != nil {
			return nil, fmt.Errorf("marshal anthropic system blocks: %w", err)
		}
		out.System = systemJSON
	}

	if tools := convertChatToolsToAnthropic(req.Tools, req.Functions); len(tools) > 0 {
		out.Tools = tools
	}

	toolChoice, err := convertChatToolChoiceToAnthropic(req.ToolChoice, req.FunctionCall)
	if err != nil {
		return nil, err
	}
	if len(toolChoice) > 0 {
		out.ToolChoice = toolChoice
	}

	if stopSeqs := decodeChatStopSequences(req.Stop); len(stopSeqs) > 0 {
		out.StopSeqs = stopSeqs
	}

	if effort := normalizeCompatReasoningEffort(req.ReasoningEffort); effort != "" {
		out.OutputConfig = &AnthropicOutputConfig{Effort: effort}
	}

	return out, nil
}

// InjectAnthropicCompatSessionMetadata injects `metadata.user_id` in the same
// JSON-string format consumed by the generic gateway sticky-session logic.
func InjectAnthropicCompatSessionMetadata(req *AnthropicRequest, sessionSeed string) {
	if req == nil {
		return
	}
	sessionSeed = strings.TrimSpace(sessionSeed)
	if sessionSeed == "" {
		return
	}
	if req.Metadata != nil && strings.TrimSpace(req.Metadata.UserID) != "" {
		return
	}
	req.Metadata = &AnthropicMetadata{
		UserID: buildAnthropicCompatUserID(sessionSeed),
	}
}

func chatUserMessageToAnthropic(msg ChatMessage) (AnthropicMessage, bool, error) {
	blocks, err := chatContentToAnthropicBlocks(msg.Content)
	if err != nil {
		return AnthropicMessage{}, false, fmt.Errorf("convert user message: %w", err)
	}
	if len(blocks) == 0 {
		return AnthropicMessage{}, false, nil
	}
	contentJSON, err := json.Marshal(blocks)
	if err != nil {
		return AnthropicMessage{}, false, err
	}
	return AnthropicMessage{Role: "user", Content: contentJSON}, true, nil
}

func chatAssistantMessageToAnthropic(msg ChatMessage) (AnthropicMessage, bool, error) {
	blocks := make([]AnthropicContentBlock, 0, 1+len(msg.ToolCalls))

	if reasoning := strings.TrimSpace(msg.ReasoningContent); reasoning != "" {
		blocks = append(blocks, AnthropicContentBlock{Type: "thinking", Thinking: reasoning})
	}

	if len(msg.Content) > 0 {
		text, err := parseAssistantContent(msg.Content)
		if err != nil {
			return AnthropicMessage{}, false, fmt.Errorf("convert assistant content: %w", err)
		}
		if strings.TrimSpace(text) != "" {
			blocks = append(blocks, AnthropicContentBlock{Type: "text", Text: text})
		}
	}

	for idx, toolCall := range msg.ToolCalls {
		toolCallID := strings.TrimSpace(toolCall.ID)
		if toolCallID == "" {
			toolCallID = fmt.Sprintf("chat_tool_%d", idx)
		}
		blocks = append(blocks, AnthropicContentBlock{
			Type:  "tool_use",
			ID:    toolCallID,
			Name:  strings.TrimSpace(toolCall.Function.Name),
			Input: normalizeToolArgumentsJSON(toolCall.Function.Arguments),
		})
	}

	if msg.FunctionCall != nil {
		functionCallID := strings.TrimSpace(msg.Name)
		if functionCallID == "" {
			functionCallID = "function_call"
		}
		blocks = append(blocks, AnthropicContentBlock{
			Type:  "tool_use",
			ID:    functionCallID,
			Name:  strings.TrimSpace(msg.FunctionCall.Name),
			Input: normalizeToolArgumentsJSON(msg.FunctionCall.Arguments),
		})
	}

	if len(blocks) == 0 {
		return AnthropicMessage{}, false, nil
	}

	contentJSON, err := json.Marshal(blocks)
	if err != nil {
		return AnthropicMessage{}, false, err
	}
	return AnthropicMessage{Role: "assistant", Content: contentJSON}, true, nil
}

func chatToolMessageToAnthropic(msg ChatMessage) (AnthropicMessage, bool, error) {
	content, err := chatToolResultContentToAnthropic(msg.Content)
	if err != nil {
		return AnthropicMessage{}, false, fmt.Errorf("convert tool message: %w", err)
	}

	toolUseID := strings.TrimSpace(msg.ToolCallID)
	if toolUseID == "" {
		toolUseID = strings.TrimSpace(msg.Name)
	}
	if toolUseID == "" {
		toolUseID = "tool_result"
	}

	contentJSON, err := json.Marshal([]AnthropicContentBlock{{
		Type:      "tool_result",
		ToolUseID: toolUseID,
		Content:   content,
	}})
	if err != nil {
		return AnthropicMessage{}, false, err
	}
	return AnthropicMessage{Role: "user", Content: contentJSON}, true, nil
}

func chatFunctionMessageToAnthropic(msg ChatMessage) (AnthropicMessage, bool, error) {
	content, err := chatToolResultContentToAnthropic(msg.Content)
	if err != nil {
		return AnthropicMessage{}, false, fmt.Errorf("convert function message: %w", err)
	}

	toolUseID := strings.TrimSpace(msg.Name)
	if toolUseID == "" {
		toolUseID = "function_result"
	}

	contentJSON, err := json.Marshal([]AnthropicContentBlock{{
		Type:      "tool_result",
		ToolUseID: toolUseID,
		Content:   content,
	}})
	if err != nil {
		return AnthropicMessage{}, false, err
	}
	return AnthropicMessage{Role: "user", Content: contentJSON}, true, nil
}

func chatContentToAnthropicBlocks(raw json.RawMessage) ([]AnthropicContentBlock, error) {
	if len(raw) == 0 {
		return nil, nil
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if strings.TrimSpace(text) == "" {
			return nil, nil
		}
		return []AnthropicContentBlock{{Type: "text", Text: text}}, nil
	}

	var parts []ChatContentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, fmt.Errorf("parse chat content parts: %w", err)
	}

	blocks := make([]AnthropicContentBlock, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case "text":
			if strings.TrimSpace(part.Text) == "" {
				continue
			}
			blocks = append(blocks, AnthropicContentBlock{Type: "text", Text: part.Text})
		case "image_url":
			if part.ImageURL == nil || strings.TrimSpace(part.ImageURL.URL) == "" {
				continue
			}
			blocks = append(blocks, AnthropicContentBlock{
				Type: "image",
				Source: &AnthropicImageSource{
					Type: "url",
					URL:  strings.TrimSpace(part.ImageURL.URL),
				},
			})
		}
	}
	return blocks, nil
}

func chatToolResultContentToAnthropic(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.Marshal("")
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return json.Marshal(text)
	}

	blocks, err := chatContentToAnthropicBlocks(raw)
	if err != nil {
		return nil, err
	}
	if len(blocks) == 0 {
		return json.Marshal("")
	}
	return json.Marshal(blocks)
}

func convertChatToolsToAnthropic(tools []ChatTool, functions []ChatFunction) []AnthropicTool {
	converted := make([]AnthropicTool, 0, len(tools)+len(functions))
	for _, tool := range tools {
		if strings.TrimSpace(tool.Type) != "function" || tool.Function == nil {
			continue
		}
		converted = append(converted, chatFunctionToAnthropicTool(*tool.Function))
	}
	for _, fn := range functions {
		converted = append(converted, chatFunctionToAnthropicTool(fn))
	}
	return converted
}

func chatFunctionToAnthropicTool(fn ChatFunction) AnthropicTool {
	inputSchema := fn.Parameters
	if len(inputSchema) == 0 {
		inputSchema = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return AnthropicTool{
		Name:        strings.TrimSpace(fn.Name),
		Description: strings.TrimSpace(fn.Description),
		InputSchema: inputSchema,
	}
}

func convertChatToolChoiceToAnthropic(toolChoice json.RawMessage, functionCall json.RawMessage) (json.RawMessage, error) {
	if len(toolChoice) > 0 {
		return normalizeChatToolChoiceToAnthropic(toolChoice)
	}
	if len(functionCall) > 0 {
		return normalizeChatFunctionCallChoiceToAnthropic(functionCall)
	}
	return nil, nil
}

func normalizeChatToolChoiceToAnthropic(raw json.RawMessage) (json.RawMessage, error) {
	var simple string
	if err := json.Unmarshal(raw, &simple); err == nil {
		return marshalAnthropicToolChoice(simple, "")
	}

	var choice map[string]any
	if err := json.Unmarshal(raw, &choice); err != nil {
		return nil, fmt.Errorf("parse chat tool_choice: %w", err)
	}
	choiceType, _ := choice["type"].(string)
	if strings.TrimSpace(choiceType) == "" {
		return nil, nil
	}
	if choiceType == "function" {
		if fn, ok := choice["function"].(map[string]any); ok {
			if name, ok := fn["name"].(string); ok {
				return marshalAnthropicToolChoice("function", name)
			}
		}
	}
	return marshalAnthropicToolChoice(choiceType, "")
}

func normalizeChatFunctionCallChoiceToAnthropic(raw json.RawMessage) (json.RawMessage, error) {
	var simple string
	if err := json.Unmarshal(raw, &simple); err == nil {
		return marshalAnthropicToolChoice(simple, "")
	}

	var choice map[string]any
	if err := json.Unmarshal(raw, &choice); err != nil {
		return nil, fmt.Errorf("parse legacy function_call: %w", err)
	}
	name, _ := choice["name"].(string)
	return marshalAnthropicToolChoice("function", name)
}

func marshalAnthropicToolChoice(choiceType, name string) (json.RawMessage, error) {
	switch strings.ToLower(strings.TrimSpace(choiceType)) {
	case "", "auto":
		return json.Marshal(map[string]any{"type": "auto"})
	case "none":
		return json.Marshal(map[string]any{"type": "none"})
	case "required", "any":
		return json.Marshal(map[string]any{"type": "any"})
	case "function", "tool":
		if strings.TrimSpace(name) == "" {
			return nil, nil
		}
		return json.Marshal(map[string]any{
			"type": "tool",
			"name": strings.TrimSpace(name),
		})
	default:
		return nil, nil
	}
}

func decodeChatStopSequences(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}

	var simple string
	if err := json.Unmarshal(raw, &simple); err == nil {
		simple = strings.TrimSpace(simple)
		if simple == "" {
			return nil
		}
		return []string{simple}
	}

	var seqs []string
	if err := json.Unmarshal(raw, &seqs); err != nil {
		return nil
	}
	out := make([]string, 0, len(seqs))
	for _, seq := range seqs {
		seq = strings.TrimSpace(seq)
		if seq != "" {
			out = append(out, seq)
		}
	}
	return out
}

func normalizeCompatReasoningEffort(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "low", "medium", "high", "max":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return ""
	}
}

func normalizeToolArgumentsJSON(arguments string) json.RawMessage {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		return json.RawMessage(`{}`)
	}
	if json.Valid([]byte(arguments)) {
		return json.RawMessage(arguments)
	}
	quoted, _ := json.Marshal(arguments)
	return json.RawMessage(quoted)
}

func buildAnthropicCompatUserID(sessionSeed string) string {
	deviceHash := sha256.Sum256([]byte("chat-compat-device:" + sessionSeed))
	deviceID := hex.EncodeToString(deviceHash[:])
	sessionID := compatUUIDFromSeed(sessionSeed)
	if strings.TrimSpace(sessionID) == "" {
		sessionID = sessionSeed
	}
	payload, _ := json.Marshal(map[string]string{
		"device_id":  deviceID,
		"session_id": sessionID,
	})
	return string(payload)
}

func compatUUIDFromSeed(seed string) string {
	if strings.TrimSpace(seed) == "" {
		return ""
	}
	hash := sha256.Sum256([]byte(seed))
	buf := make([]byte, 16)
	copy(buf, hash[:16])
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}
