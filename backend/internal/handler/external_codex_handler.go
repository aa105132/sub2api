package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type ExternalCodexHandler struct {
	service *service.CodexExternalService
}

func NewExternalCodexHandler(service *service.CodexExternalService) *ExternalCodexHandler {
	return &ExternalCodexHandler{service: service}
}

func (h *ExternalCodexHandler) PublicStatus(c *gin.Context) {
	if h == nil || h.service == nil {
		writeExternalCodexDetail(c, http.StatusServiceUnavailable, "Codex 外部服务不可用")
		return
	}
	c.JSON(http.StatusOK, h.service.PublicStatus())
}

func (h *ExternalCodexHandler) AuthURL(c *gin.Context) {
	if h == nil || h.service == nil {
		writeExternalCodexDetail(c, http.StatusServiceUnavailable, "Codex 外部服务不可用")
		return
	}
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		writeExternalCodexDetail(c, http.StatusUnauthorized, "管理员认证上下文缺失")
		return
	}
	result, err := h.service.GenerateAuthURL(c.Request.Context(), subject.UserID)
	if err != nil {
		writeExternalCodexError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *ExternalCodexHandler) Callback(c *gin.Context) {
	if h == nil || h.service == nil {
		writeExternalCodexDetail(c, http.StatusServiceUnavailable, "Codex 外部服务不可用")
		return
	}
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		writeExternalCodexDetail(c, http.StatusUnauthorized, "管理员认证上下文缺失")
		return
	}
	var input service.CodexExternalCallbackInput
	if !bindExternalCodexJSON(c, &input) {
		return
	}
	input.UserID = subject.UserID
	result, err := h.service.Callback(c.Request.Context(), input)
	if err != nil {
		writeExternalCodexError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *ExternalCodexHandler) DirectPush(c *gin.Context) {
	if h == nil || h.service == nil {
		writeExternalCodexDetail(c, http.StatusServiceUnavailable, "Codex 外部服务不可用")
		return
	}
	var input service.CodexExternalDirectPushInput
	if !bindExternalCodexJSON(c, &input) {
		return
	}
	fillExternalCodexAuthInput(c, &input.CodexExternalAuthInput)
	result, err := h.service.DirectPush(c.Request.Context(), input)
	if err != nil {
		writeExternalCodexError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *ExternalCodexHandler) Status(c *gin.Context) {
	if h == nil || h.service == nil {
		writeExternalCodexDetail(c, http.StatusServiceUnavailable, "Codex 外部服务不可用")
		return
	}
	var input service.CodexExternalAuthInput
	if !bindExternalCodexJSON(c, &input) {
		return
	}
	fillExternalCodexAuthInput(c, &input)
	result, err := h.service.Status(c.Request.Context(), input)
	if err != nil {
		writeExternalCodexError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *ExternalCodexHandler) TeamVacancies(c *gin.Context) {
	if h == nil || h.service == nil {
		writeExternalCodexDetail(c, http.StatusServiceUnavailable, "Codex 外部服务不可用")
		return
	}
	var input service.CodexExternalAuthInput
	if !bindExternalCodexJSON(c, &input) {
		return
	}
	fillExternalCodexAuthInput(c, &input)
	result, err := h.service.TeamVacancies(c.Request.Context(), input)
	if err != nil {
		writeExternalCodexError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *ExternalCodexHandler) TeamInfo(c *gin.Context) {
	if h == nil || h.service == nil {
		writeExternalCodexDetail(c, http.StatusServiceUnavailable, "Codex 外部服务不可用")
		return
	}
	var input service.CodexExternalTeamInfoInput
	if !bindExternalCodexJSON(c, &input) {
		return
	}
	fillExternalCodexAuthInput(c, &input.CodexExternalAuthInput)
	result, err := h.service.TeamInfo(c.Request.Context(), input)
	if err != nil {
		writeExternalCodexError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *ExternalCodexHandler) TeamInvite(c *gin.Context) {
	if h == nil || h.service == nil {
		writeExternalCodexDetail(c, http.StatusServiceUnavailable, "Codex 外部服务不可用")
		return
	}
	var input service.CodexExternalTeamInviteInput
	if !bindExternalCodexJSON(c, &input) {
		return
	}
	fillExternalCodexAuthInput(c, &input.CodexExternalAuthInput)
	result, err := h.service.TeamInvite(c.Request.Context(), input)
	if err != nil {
		writeExternalCodexError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *ExternalCodexHandler) TeamKick(c *gin.Context) {
	if h == nil || h.service == nil {
		writeExternalCodexDetail(c, http.StatusServiceUnavailable, "Codex 外部服务不可用")
		return
	}
	var input service.CodexExternalTeamKickInput
	if !bindExternalCodexJSON(c, &input) {
		return
	}
	fillExternalCodexAuthInput(c, &input.CodexExternalAuthInput)
	result, err := h.service.TeamKick(c.Request.Context(), input)
	if err != nil {
		writeExternalCodexError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *ExternalCodexHandler) TeamCleanup(c *gin.Context) {
	if h == nil || h.service == nil {
		writeExternalCodexDetail(c, http.StatusServiceUnavailable, "Codex 外部服务不可用")
		return
	}
	var input service.CodexExternalTeamCleanupInput
	if !bindExternalCodexJSON(c, &input) {
		return
	}
	fillExternalCodexAuthInput(c, &input.CodexExternalAuthInput)
	result, err := h.service.TeamCleanup(c.Request.Context(), input)
	if err != nil {
		writeExternalCodexError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func bindExternalCodexJSON(c *gin.Context, target any) bool {
	if c == nil || target == nil || c.Request == nil || c.Request.Body == nil {
		return true
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	if err != nil {
		writeExternalCodexDetail(c, http.StatusBadRequest, "读取请求体失败")
		return false
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	if len(bytes.TrimSpace(body)) == 0 {
		return true
	}
	if err := json.Unmarshal(body, target); err != nil {
		writeExternalCodexDetail(c, http.StatusBadRequest, "请求体 JSON 无效: "+err.Error())
		return false
	}
	fillExternalCodexCompatInput(target, body)
	return true
}

func fillExternalCodexCompatInput(target any, body []byte) {
	if target == nil || len(bytes.TrimSpace(body)) == 0 {
		return
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return
	}

	switch input := target.(type) {
	case *service.CodexExternalAuthInput:
		fillExternalCodexAuthAliases(payload, input)
	case *service.CodexExternalCallbackInput:
		fillInt64JSONAlias(payload, &input.UserID, "user_id", "userId")
		fillStringJSONAlias(payload, &input.CallbackURL, "callback_url", "callbackUrl")
		fillStringJSONAlias(payload, &input.State, "state")
		fillBoolJSONAlias(payload, &input.IsPublic, "is_public", "isPublic")
	case *service.CodexExternalDirectPushInput:
		fillExternalCodexAuthAliases(payload, &input.CodexExternalAuthInput)
		fillStringJSONAlias(payload, &input.AccessToken, "access_token", "accessToken")
		fillStringJSONAlias(payload, &input.RefreshToken, "refresh_token", "refreshToken")
		fillStringJSONAlias(payload, &input.AccountID, "account_id", "accountId")
		fillStringJSONAlias(payload, &input.PlanType, "plan_type", "planType")
		fillStringJSONAlias(payload, &input.TeamAccountID, "team_account_id", "teamAccountId")
		fillInt64JSONAlias(payload, &input.TeamOwnerCredentialID, "team_owner_credential_id", "teamOwnerCredentialId")
		fillBoolJSONAlias(payload, &input.IsPublic, "is_public", "isPublic")
	case *service.CodexExternalTeamInfoInput:
		fillExternalCodexAuthAliases(payload, &input.CodexExternalAuthInput)
		fillInt64JSONAlias(payload, &input.OwnerCredentialID, "owner_credential_id", "ownerCredentialId")
		fillStringJSONAlias(payload, &input.TeamAccountID, "team_account_id", "teamAccountId")
		fillBoolJSONAlias(payload, &input.IncludeMembers, "include_members", "includeMembers")
	case *service.CodexExternalTeamInviteInput:
		fillExternalCodexAuthAliases(payload, &input.CodexExternalAuthInput)
		fillInt64JSONAlias(payload, &input.OwnerCredentialID, "owner_credential_id", "ownerCredentialId")
	case *service.CodexExternalTeamKickInput:
		fillExternalCodexAuthAliases(payload, &input.CodexExternalAuthInput)
		fillInt64JSONAlias(payload, &input.OwnerCredentialID, "owner_credential_id", "ownerCredentialId")
		fillStringJSONAlias(payload, &input.TeamAccountID, "team_account_id", "teamAccountId")
		fillStringJSONAlias(payload, &input.TeamMemberUserID, "team_member_user_id", "teamMemberUserId")
	}
}

func fillExternalCodexAuthAliases(payload map[string]json.RawMessage, input *service.CodexExternalAuthInput) {
	if input == nil {
		return
	}
	fillStringJSONAlias(payload, &input.APIKey, "api_key", "apiKey")
	fillStringJSONAlias(payload, &input.AdminPassword, "admin_password", "adminPassword")
}

func fillStringJSONAlias(payload map[string]json.RawMessage, target *string, keys ...string) {
	if target == nil {
		return
	}

	value, ok := lookupJSONAlias(payload, keys...)
	if !ok {
		return
	}

	var decoded string
	if err := json.Unmarshal(value, &decoded); err == nil {
		*target = decoded
	}
}

func fillInt64JSONAlias(payload map[string]json.RawMessage, target *int64, keys ...string) {
	if target == nil {
		return
	}

	value, ok := lookupJSONAlias(payload, keys...)
	if !ok {
		return
	}

	var decoded int64
	if err := json.Unmarshal(value, &decoded); err == nil {
		*target = decoded
		return
	}

	var text string
	if err := json.Unmarshal(value, &text); err != nil {
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		*target = 0
		return
	}
	parsed, err := strconv.ParseInt(text, 10, 64)
	if err == nil {
		*target = parsed
	}
}

func fillBoolJSONAlias(payload map[string]json.RawMessage, target *bool, keys ...string) {
	if target == nil {
		return
	}

	value, ok := lookupJSONAlias(payload, keys...)
	if !ok {
		return
	}

	var decoded bool
	if err := json.Unmarshal(value, &decoded); err == nil {
		*target = decoded
		return
	}

	var text string
	if err := json.Unmarshal(value, &text); err != nil {
		return
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(text))
	if err == nil {
		*target = parsed
	}
}

func lookupJSONAlias(payload map[string]json.RawMessage, keys ...string) (json.RawMessage, bool) {
	for _, key := range keys {
		value, ok := payload[key]
		if ok {
			return value, true
		}
	}
	return nil, false
}

func fillExternalCodexAuthInput(c *gin.Context, input *service.CodexExternalAuthInput) {
	if c == nil || input == nil {
		return
	}
	if strings.TrimSpace(input.APIKey) == "" {
		input.APIKey = firstNonEmpty(
			c.GetHeader("X-API-Key"),
			c.GetHeader("X-External-API-Key"),
			c.Query("api_key"),
		)
	}
	if strings.TrimSpace(input.AdminPassword) == "" {
		input.AdminPassword = firstNonEmpty(
			c.GetHeader("X-Admin-Password"),
			c.Query("admin_password"),
		)
	}
}

func writeExternalCodexError(c *gin.Context, err error) {
	status := infraerrors.Code(err)
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	detail := strings.TrimSpace(infraerrors.Message(err))
	if detail == "" && err != nil {
		detail = strings.TrimSpace(err.Error())
	}
	if detail == "" {
		detail = "内部错误"
	}
	writeExternalCodexDetail(c, status, detail)
}

func writeExternalCodexDetail(c *gin.Context, status int, detail string) {
	c.JSON(status, gin.H{"detail": detail})
}
