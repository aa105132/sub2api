package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func buildResponsesCompletedSSE(model, text string) string {
	return strings.Join([]string{
		fmt.Sprintf(
			`data: {"type":"response.completed","response":{"id":"resp_test","object":"response","model":%q,"status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":%q}]}],"usage":{"input_tokens":11,"output_tokens":7,"total_tokens":18}}}`,
			model,
			text,
		),
		"",
		"data: [DONE]",
		"",
	}, "\n")
}

func newOpenAIOAuthCompatTestAccount() *Account {
	return &Account{
		ID:             123,
		Name:           "codex-oauth",
		Platform:       PlatformOpenAI,
		Type:           AccountTypeOAuth,
		Concurrency:    1,
		Credentials:    map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
		Status:         StatusActive,
		Schedulable:    true,
		RateMultiplier: f64p(1),
	}
}

func newOpenAICompatTestService(upstream *httpUpstreamRecorder) *OpenAIGatewayService {
	return &OpenAIGatewayService{
		cfg:          &config.Config{Gateway: config.GatewayConfig{ForceCodexCLI: false}},
		httpUpstream: upstream,
	}
}

func TestOpenAIGatewayService_ForwardAsChatCompletions_OAuthCodexTransformsRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(nil))
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.1.0")

	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"x-request-id": []string{"rid-chat"},
			},
			Body: io.NopCloser(strings.NewReader(buildResponsesCompletedSSE("gpt-5.3-codex", "你好，世界"))),
		},
	}
	svc := newOpenAICompatTestService(upstream)
	account := newOpenAIOAuthCompatTestAccount()

	body := []byte(`{
		"model":"gpt-5.3",
		"stream":false,
		"temperature":0.2,
		"top_p":0.5,
		"max_completion_tokens":64,
		"messages":[
			{"role":"system","content":"你是代码助手"},
			{"role":"user","content":"你好"}
		]
	}`)

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "chat-session", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Stream)
	require.Equal(t, "gpt-5.3", result.Model)

	require.NotNil(t, upstream.lastReq)
	require.Equal(t, chatgptCodexURL, upstream.lastReq.URL.String())
	require.Equal(t, "Bearer oauth-token", upstream.lastReq.Header.Get("Authorization"))
	require.NotEmpty(t, upstream.lastReq.Header.Get("session_id"))

	require.Equal(t, "gpt-5.3-codex", gjson.GetBytes(upstream.lastBody, "model").String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "store").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "store").Bool())
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.Equal(t, "chat-session", gjson.GetBytes(upstream.lastBody, "prompt_cache_key").String())
	require.Equal(t, "你是代码助手", gjson.GetBytes(upstream.lastBody, "instructions").String())
	require.Equal(t, "user", gjson.GetBytes(upstream.lastBody, "input.0.role").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "temperature").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "top_p").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "max_output_tokens").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "max_completion_tokens").Exists())

	require.Equal(t, "chat.completion", gjson.GetBytes(rec.Body.Bytes(), "object").String())
	require.Equal(t, "你好，世界", gjson.GetBytes(rec.Body.Bytes(), "choices.0.message.content").String())
}

func TestOpenAIGatewayService_ForwardAsAnthropic_OAuthCodexTransformsRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(nil))
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.1.0")
	c.Set("api_key", &APIKey{ID: 456})

	upstream := &httpUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"text/event-stream"},
				"x-request-id": []string{"rid-msg"},
			},
			Body: io.NopCloser(strings.NewReader(buildResponsesCompletedSSE("gpt-5.3-codex", "Anthropic 兼容成功"))),
		},
	}
	svc := newOpenAICompatTestService(upstream)
	account := newOpenAIOAuthCompatTestAccount()

	body := []byte(`{
		"model":"gpt-5.3",
		"max_tokens":64,
		"stream":false,
		"system":"你是代码助手",
		"messages":[
			{"role":"user","content":"你好"}
		]
	}`)

	result, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "msg-session", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Stream)
	require.Equal(t, "gpt-5.3", result.Model)

	require.NotNil(t, upstream.lastReq)
	require.Equal(t, chatgptCodexURL, upstream.lastReq.URL.String())
	require.Equal(t, "Bearer oauth-token", upstream.lastReq.Header.Get("Authorization"))
	require.NotEmpty(t, upstream.lastReq.Header.Get("session_id"))

	require.Equal(t, "gpt-5.3-codex", gjson.GetBytes(upstream.lastBody, "model").String())
	require.True(t, gjson.GetBytes(upstream.lastBody, "store").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "store").Bool())
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.Equal(t, "msg-session", gjson.GetBytes(upstream.lastBody, "prompt_cache_key").String())
	require.Equal(t, "你是代码助手", gjson.GetBytes(upstream.lastBody, "instructions").String())
	require.Equal(t, "user", gjson.GetBytes(upstream.lastBody, "input.0.role").String())
	require.False(t, gjson.GetBytes(upstream.lastBody, "max_output_tokens").Exists())

	require.Equal(t, "message", gjson.GetBytes(rec.Body.Bytes(), "type").String())
	require.Equal(t, "Anthropic 兼容成功", gjson.GetBytes(rec.Body.Bytes(), "content.0.text").String())
}
