package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestBindExternalCodexJSONDirectPushSnakeCase(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := `{
		"api_key":"external-key",
		"admin_password":"admin-pass",
		"access_token":"access-token",
		"refresh_token":"refresh-token",
		"email":"user@example.com",
		"account_id":"acct_123",
		"plan_type":"team",
		"team_account_id":"team_123",
		"team_owner_credential_id":42,
		"is_public":true
	}`

	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "/api/external/codex/direct-push", strings.NewReader(body))

	var input service.CodexExternalDirectPushInput
	require.True(t, bindExternalCodexJSON(context, &input))
	require.Equal(t, "external-key", input.APIKey)
	require.Equal(t, "admin-pass", input.AdminPassword)
	require.Equal(t, "access-token", input.AccessToken)
	require.Equal(t, "refresh-token", input.RefreshToken)
	require.Equal(t, "user@example.com", input.Email)
	require.Equal(t, "acct_123", input.AccountID)
	require.Equal(t, "team", input.PlanType)
	require.Equal(t, "team_123", input.TeamAccountID)
	require.EqualValues(t, 42, input.TeamOwnerCredentialID)
	require.True(t, input.IsPublic)
}

func TestBindExternalCodexJSONTeamInfoSnakeCaseStringValues(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := `{
		"api_key":"external-key",
		"owner_credential_id":"23",
		"team_account_id":"team_abc",
		"include_members":"true"
	}`

	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(http.MethodPost, "/api/external/codex/team/info", strings.NewReader(body))

	var input service.CodexExternalTeamInfoInput
	require.True(t, bindExternalCodexJSON(context, &input))
	require.Equal(t, "external-key", input.APIKey)
	require.EqualValues(t, 23, input.OwnerCredentialID)
	require.Equal(t, "team_abc", input.TeamAccountID)
	require.True(t, input.IncludeMembers)
}
