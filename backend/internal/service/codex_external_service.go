package service

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const defaultCodexExternalCallbackPort = 1455

type codexExternalAuthSession struct {
	SessionID   string
	UserID      int64
	RedirectURI string
	CreatedAt   time.Time
}

type CodexExternalAuthInput struct {
	APIKey        string
	AdminPassword string
}

type CodexExternalCallbackInput struct {
	UserID      int64
	CallbackURL string
	State       string
	IsPublic    bool
}

type CodexExternalDirectPushInput struct {
	CodexExternalAuthInput
	AccessToken           string
	RefreshToken          string
	Email                 string
	AccountID             string
	PlanType              string
	TeamAccountID         string
	TeamOwnerCredentialID int64
	IsPublic              bool
}

type CodexExternalTeamInfoInput struct {
	CodexExternalAuthInput
	OwnerCredentialID int64
	TeamAccountID     string
	IncludeMembers    bool
}

type CodexExternalTeamInviteInput struct {
	CodexExternalAuthInput
	OwnerCredentialID int64
	Emails            []string
}

type CodexExternalTeamKickInput struct {
	CodexExternalAuthInput
	OwnerCredentialID int64
	TeamAccountID     string
	TeamMemberUserID  string
	Email             string
}

type CodexExternalTeamCleanupInput = CodexExternalTeamKickInput

type codexAccountUpsertInput struct {
	AccessToken           string
	RefreshToken          string
	Email                 string
	AccountID             string
	PlanType              string
	TeamAccountID         string
	TeamOwnerCredentialID int64
	IsPublic              bool
	Source                string
}

type teamOwnerSnapshot struct {
	Owner    *Account
	Snapshot *ChatGPTTeamSnapshot
}

type CodexExternalService struct {
	cfg                *config.Config
	adminService       AdminService
	accountRepo        AccountRepository
	openaiOAuthService *OpenAIOAuthService
	teamService        *OpenAIChatGPTTeamService

	authSessions sync.Map
}

func NewCodexExternalService(
	cfg *config.Config,
	adminService AdminService,
	accountRepo AccountRepository,
	openaiOAuthService *OpenAIOAuthService,
	teamService *OpenAIChatGPTTeamService,
) *CodexExternalService {
	return &CodexExternalService{
		cfg:                cfg,
		adminService:       adminService,
		accountRepo:        accountRepo,
		openaiOAuthService: openaiOAuthService,
		teamService:        teamService,
	}
}

func normalizeCodexPlanType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "free":
		return "free"
	case "plus":
		return "plus"
	case "pro":
		return "pro"
	case "team", "enterprise", "business":
		return "team"
	default:
		return "free"
	}
}

func isCodexTeamPlanType(value string) bool {
	return normalizeCodexPlanType(value) == "team"
}

func (s *CodexExternalService) PublicStatus() map[string]any {
	return map[string]any{
		"success": true,
		"enabled": s != nil && s.openaiOAuthService != nil,
	}
}

func (s *CodexExternalService) ValidateExternalAuth(_ context.Context, input CodexExternalAuthInput) error {
	apiKey := strings.TrimSpace(input.APIKey)
	adminPassword := strings.TrimSpace(input.AdminPassword)

	expectedAPIKey := ""
	expectedAdminPassword := ""
	if s != nil && s.cfg != nil {
		expectedAPIKey = strings.TrimSpace(s.cfg.Default.CodexExternalAPIKey)
		expectedAdminPassword = strings.TrimSpace(s.cfg.Default.AdminPassword)
	}

	if apiKey != "" {
		if expectedAPIKey != "" && subtle.ConstantTimeCompare([]byte(apiKey), []byte(expectedAPIKey)) == 1 {
			return nil
		}
		if expectedAPIKey == "" && expectedAdminPassword != "" &&
			subtle.ConstantTimeCompare([]byte(apiKey), []byte(expectedAdminPassword)) == 1 {
			return nil
		}
	}

	if adminPassword != "" && expectedAdminPassword != "" &&
		subtle.ConstantTimeCompare([]byte(adminPassword), []byte(expectedAdminPassword)) == 1 {
		return nil
	}

	return infraerrors.New(http.StatusForbidden, "CODEX_EXTERNAL_AUTH_FAILED", "认证失败：请提供有效的 api_key 或 admin_password")
}

func (s *CodexExternalService) GenerateAuthURL(ctx context.Context, userID int64) (map[string]any, error) {
	if s == nil || s.openaiOAuthService == nil {
		return nil, infraerrors.New(http.StatusServiceUnavailable, "CODEX_EXTERNAL_UNAVAILABLE", "OpenAI OAuth 服务不可用")
	}
	if userID <= 0 {
		return nil, infraerrors.New(http.StatusUnauthorized, "CODEX_EXTERNAL_INVALID_USER", "用户未认证")
	}

	redirectURI := fmt.Sprintf("http://localhost:%d/auth/callback", defaultCodexExternalCallbackPort)
	result, err := s.openaiOAuthService.GenerateAuthURL(ctx, nil, redirectURI, PlatformOpenAI)
	if err != nil {
		return nil, err
	}

	parsedURL, err := url.Parse(result.AuthURL)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "CODEX_EXTERNAL_AUTH_URL_INVALID", "生成的授权地址无效: %v", err)
	}
	state := strings.TrimSpace(parsedURL.Query().Get("state"))
	if state == "" {
		return nil, infraerrors.New(http.StatusInternalServerError, "CODEX_EXTERNAL_STATE_MISSING", "生成授权地址失败：缺少 state")
	}

	s.authSessions.Store(state, codexExternalAuthSession{
		SessionID:   result.SessionID,
		UserID:      userID,
		RedirectURI: redirectURI,
		CreatedAt:   time.Now(),
	})

	return map[string]any{
		"success":       true,
		"auth_url":      result.AuthURL,
		"state":         state,
		"session_id":    result.SessionID,
		"callback_port": defaultCodexExternalCallbackPort,
		"redirect_uri":  redirectURI,
	}, nil
}

func (s *CodexExternalService) Callback(ctx context.Context, input CodexExternalCallbackInput) (map[string]any, error) {
	if s == nil || s.openaiOAuthService == nil {
		return nil, infraerrors.New(http.StatusServiceUnavailable, "CODEX_EXTERNAL_UNAVAILABLE", "OpenAI OAuth 服务不可用")
	}

	callbackURL := strings.TrimSpace(input.CallbackURL)
	if callbackURL == "" {
		return nil, infraerrors.New(http.StatusBadRequest, "CODEX_EXTERNAL_CALLBACK_URL_REQUIRED", "callback_url 不能为空")
	}

	parsedURL, err := url.Parse(callbackURL)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadRequest, "CODEX_EXTERNAL_CALLBACK_URL_INVALID", "callback_url 无效: %v", err)
	}

	query := parsedURL.Query()
	if providerErr := strings.TrimSpace(query.Get("error")); providerErr != "" {
		return nil, infraerrors.Newf(http.StatusBadRequest, "CODEX_EXTERNAL_OAUTH_ERROR", "OAuth 错误: %s - %s", providerErr, strings.TrimSpace(query.Get("error_description")))
	}

	code := strings.TrimSpace(query.Get("code"))
	if code == "" {
		return nil, infraerrors.New(http.StatusBadRequest, "CODEX_EXTERNAL_CODE_MISSING", "回调 URL 中未找到授权码")
	}

	effectiveState := strings.TrimSpace(query.Get("state"))
	if effectiveState == "" {
		effectiveState = strings.TrimSpace(input.State)
	}
	if effectiveState == "" {
		return nil, infraerrors.New(http.StatusBadRequest, "CODEX_EXTERNAL_STATE_MISSING", "未找到 state 参数")
	}

	rawSession, ok := s.authSessions.Load(effectiveState)
	if !ok {
		return nil, infraerrors.New(http.StatusBadRequest, "CODEX_EXTERNAL_STATE_INVALID", "无效或已过期的 state，请重新获取授权链接")
	}
	session, _ := rawSession.(codexExternalAuthSession)
	if session.UserID != input.UserID {
		return nil, infraerrors.New(http.StatusForbidden, "CODEX_EXTERNAL_STATE_FORBIDDEN", "state 不属于当前用户")
	}
	s.authSessions.Delete(effectiveState)

	tokenInfo, err := s.openaiOAuthService.ExchangeCode(ctx, &OpenAIExchangeCodeInput{
		SessionID:   session.SessionID,
		Code:        code,
		State:       effectiveState,
		RedirectURI: session.RedirectURI,
	})
	if err != nil {
		return nil, err
	}

	return s.upsertCodexAccount(ctx, codexAccountUpsertInput{
		AccessToken:  tokenInfo.AccessToken,
		RefreshToken: tokenInfo.RefreshToken,
		Email:        tokenInfo.Email,
		AccountID:    tokenInfo.ChatGPTAccountID,
		PlanType:     tokenInfo.PlanType,
		IsPublic:     input.IsPublic,
		Source:       "callback",
	})
}

func (s *CodexExternalService) DirectPush(ctx context.Context, input CodexExternalDirectPushInput) (map[string]any, error) {
	if err := s.ValidateExternalAuth(ctx, input.CodexExternalAuthInput); err != nil {
		return nil, err
	}

	return s.upsertCodexAccount(ctx, codexAccountUpsertInput{
		AccessToken:           input.AccessToken,
		RefreshToken:          input.RefreshToken,
		Email:                 input.Email,
		AccountID:             input.AccountID,
		PlanType:              input.PlanType,
		TeamAccountID:         input.TeamAccountID,
		TeamOwnerCredentialID: input.TeamOwnerCredentialID,
		IsPublic:              input.IsPublic,
		Source:                "direct_push",
	})
}

func (s *CodexExternalService) Status(ctx context.Context, input CodexExternalAuthInput) (map[string]any, error) {
	if err := s.ValidateExternalAuth(ctx, input); err != nil {
		return nil, err
	}
	accounts, err := s.listCodexAccounts(ctx)
	if err != nil {
		return nil, err
	}
	teamPool, err := s.collectTeamPoolStatus(ctx)
	if err != nil {
		return nil, err
	}
	payload := buildExternalCodexStatusPayload(accounts, teamPool)
	payload["success"] = true
	return payload, nil
}

func (s *CodexExternalService) TeamVacancies(ctx context.Context, input CodexExternalAuthInput) (map[string]any, error) {
	if err := s.ValidateExternalAuth(ctx, input); err != nil {
		return nil, err
	}
	teamPool, err := s.collectTeamPoolStatus(ctx)
	if err != nil {
		return nil, err
	}
	teamPool["success"] = true
	return teamPool, nil
}

func (s *CodexExternalService) TeamInfo(ctx context.Context, input CodexExternalTeamInfoInput) (map[string]any, error) {
	if err := s.ValidateExternalAuth(ctx, input.CodexExternalAuthInput); err != nil {
		return nil, err
	}
	if s.teamService == nil {
		return nil, infraerrors.New(http.StatusServiceUnavailable, "CODEX_TEAM_SERVICE_UNAVAILABLE", "Team 服务不可用")
	}

	owner, err := s.resolveTeamOwnerAccount(ctx, input.OwnerCredentialID, input.TeamAccountID)
	if err != nil {
		return nil, err
	}
	snapshot, err := s.teamService.GetTeamSnapshot(ctx, owner, input.IncludeMembers)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"success": true,
		"team_info": map[string]any{
			"owner_credential_id": snapshot.OwnerAccountID,
			"owner_email":         snapshot.OwnerEmail,
			"owner_name":          snapshot.OwnerName,
			"team_account_id":     snapshot.TeamAccountID,
			"team_name":           snapshot.TeamName,
			"is_team_admin":       snapshot.IsTeamAdmin,
			"member_limit":        snapshot.MemberLimit,
			"member_count":        snapshot.MemberCount,
			"invite_count":        snapshot.InviteCount,
			"vacancies":           snapshot.Vacancies,
			"members":             snapshot.Members,
			"invites":             snapshot.Invites,
		},
	}, nil
}

func (s *CodexExternalService) TeamInvite(ctx context.Context, input CodexExternalTeamInviteInput) (map[string]any, error) {
	if err := s.ValidateExternalAuth(ctx, input.CodexExternalAuthInput); err != nil {
		return nil, err
	}
	if s.teamService == nil {
		return nil, infraerrors.New(http.StatusServiceUnavailable, "CODEX_TEAM_SERVICE_UNAVAILABLE", "Team 服务不可用")
	}

	emails := normalizeEmailList(input.Emails)
	if len(emails) == 0 {
		return nil, infraerrors.New(http.StatusBadRequest, "CODEX_EXTERNAL_EMAILS_REQUIRED", "emails 不能为空")
	}

	assignments := make(map[int64][]string)
	results := make([]map[string]any, 0, len(emails))

	if input.OwnerCredentialID > 0 {
		owner, err := s.resolveTeamOwnerAccount(ctx, input.OwnerCredentialID, "")
		if err != nil {
			return nil, err
		}
		snapshot, err := s.teamService.GetTeamSnapshot(ctx, owner, false)
		if err != nil {
			return nil, err
		}
		remaining := snapshot.Vacancies
		for _, email := range emails {
			if remaining <= 0 {
				results = append(results, map[string]any{
					"success":             false,
					"email":               email,
					"owner_credential_id": owner.ID,
					"team_account_id":     snapshot.TeamAccountID,
					"error":               "no available team slot",
				})
				continue
			}
			assignments[owner.ID] = append(assignments[owner.ID], email)
			results = append(results, map[string]any{
				"success":             true,
				"email":               email,
				"owner_credential_id": owner.ID,
				"team_account_id":     snapshot.TeamAccountID,
			})
			remaining--
		}
	} else {
		owners, err := s.collectTeamOwnerSnapshots(ctx)
		if err != nil {
			return nil, err
		}
		sort.SliceStable(owners, func(i, j int) bool {
			if owners[i].Snapshot.Vacancies != owners[j].Snapshot.Vacancies {
				return owners[i].Snapshot.Vacancies > owners[j].Snapshot.Vacancies
			}
			return owners[i].Owner.ID < owners[j].Owner.ID
		})
		for _, email := range emails {
			assigned := false
			for i := range owners {
				if owners[i].Snapshot.Vacancies <= 0 {
					continue
				}
				assignments[owners[i].Owner.ID] = append(assignments[owners[i].Owner.ID], email)
				results = append(results, map[string]any{
					"success":             true,
					"email":               email,
					"owner_credential_id": owners[i].Owner.ID,
					"team_account_id":     owners[i].Snapshot.TeamAccountID,
				})
				owners[i].Snapshot.Vacancies--
				assigned = true
				break
			}
			if !assigned {
				results = append(results, map[string]any{
					"success": false,
					"email":   email,
					"error":   "no available team slot",
				})
			}
		}
	}

	failedByEmail := make(map[string]string)
	for ownerID, ownerEmails := range assignments {
		if len(ownerEmails) == 0 {
			continue
		}
		owner, err := s.accountRepo.GetByID(ctx, ownerID)
		if err != nil {
			for _, email := range ownerEmails {
				failedByEmail[email] = err.Error()
			}
			continue
		}
		if err := s.teamService.InviteMembers(ctx, owner, ownerEmails); err != nil {
			for _, email := range ownerEmails {
				failedByEmail[email] = err.Error()
			}
		}
	}

	invited := make([]map[string]any, 0, len(results))
	failed := make([]map[string]any, 0)
	finalResults := make([]map[string]any, 0, len(results))
	for _, item := range results {
		email := strings.ToLower(strings.TrimSpace(fmt.Sprint(item["email"])))
		if reason, ok := failedByEmail[email]; ok {
			item["success"] = false
			item["error"] = reason
		}
		finalResults = append(finalResults, item)
		if success, _ := item["success"].(bool); success {
			invited = append(invited, item)
		} else {
			failed = append(failed, item)
		}
	}

	return map[string]any{
		"success": !hasInviteFailure(finalResults),
		"invited": invited,
		"failed":  failed,
		"results": finalResults,
	}, nil
}

func (s *CodexExternalService) TeamKick(ctx context.Context, input CodexExternalTeamKickInput) (map[string]any, error) {
	if err := s.ValidateExternalAuth(ctx, input.CodexExternalAuthInput); err != nil {
		return nil, err
	}
	if s.teamService == nil {
		return nil, infraerrors.New(http.StatusServiceUnavailable, "CODEX_TEAM_SERVICE_UNAVAILABLE", "Team 服务不可用")
	}

	owner, err := s.resolveTeamOwnerAccount(ctx, input.OwnerCredentialID, input.TeamAccountID)
	if err != nil {
		return nil, err
	}

	action, err := s.teamService.RemoveMemberOrInvite(
		ctx,
		owner,
		input.TeamAccountID,
		strings.TrimSpace(input.TeamMemberUserID),
		strings.TrimSpace(input.Email),
	)
	if err != nil {
		return nil, err
	}

	localRemoved := false
	if localAccount := s.findLocalTeamMemberAccount(
		ctx,
		strings.TrimSpace(input.Email),
		strings.TrimSpace(input.TeamMemberUserID),
		getCodexTeamAccountID(owner),
	); localAccount != nil {
		if err := s.deleteLocalCodexAccount(ctx, localAccount, "manual team kick"); err == nil {
			localRemoved = true
		}
	}

	return map[string]any{
		"success":               true,
		"action":                action,
		"owner_credential_id":   owner.ID,
		"team_account_id":       getCodexTeamAccountID(owner),
		"email":                 strings.TrimSpace(input.Email),
		"team_member_user_id":   strings.TrimSpace(input.TeamMemberUserID),
		"local_account_removed": localRemoved,
	}, nil
}

func (s *CodexExternalService) TeamCleanup(ctx context.Context, input CodexExternalTeamCleanupInput) (map[string]any, error) {
	return s.TeamKick(ctx, CodexExternalTeamKickInput(input))
}

func (s *CodexExternalService) upsertCodexAccount(ctx context.Context, input codexAccountUpsertInput) (map[string]any, error) {
	if s == nil || s.accountRepo == nil || s.adminService == nil {
		return nil, infraerrors.New(http.StatusServiceUnavailable, "CODEX_EXTERNAL_UNAVAILABLE", "Codex 外部服务未正确初始化")
	}

	accessToken := strings.TrimSpace(input.AccessToken)
	refreshToken := strings.TrimSpace(input.RefreshToken)
	if accessToken == "" {
		return nil, infraerrors.New(http.StatusBadRequest, "CODEX_EXTERNAL_ACCESS_TOKEN_REQUIRED", "access_token 不能为空")
	}
	if refreshToken == "" {
		return nil, infraerrors.New(http.StatusBadRequest, "CODEX_EXTERNAL_REFRESH_TOKEN_REQUIRED", "refresh_token 不能为空")
	}

	var workspaceInfo *ChatGPTWorkspaceInfo
	var resolveErr error
	if s.teamService != nil {
		workspaceInfo, resolveErr = s.teamService.ResolveWorkspaceInfo(
			ctx,
			accessToken,
			input.PlanType,
			input.AccountID,
			input.Email,
			input.TeamAccountID,
		)
	}

	effectivePlan := normalizeCodexPlanType(input.PlanType)
	resolvedEmail := strings.TrimSpace(input.Email)
	resolvedAccountID := strings.TrimSpace(input.AccountID)
	resolvedTeamAccountID := strings.TrimSpace(input.TeamAccountID)
	chatGPTUserID := ""
	teamName := ""
	isTeamWorkspace := false
	isTeamAdmin := false

	if workspaceInfo != nil {
		if plan := normalizeCodexPlanType(workspaceInfo.EffectivePlanType); plan != "" {
			effectivePlan = plan
		}
		if email := strings.TrimSpace(workspaceInfo.Email); email != "" {
			resolvedEmail = email
		}
		if accountID := strings.TrimSpace(workspaceInfo.AccountID); accountID != "" {
			resolvedAccountID = accountID
		}
		if teamAccountID := strings.TrimSpace(workspaceInfo.TeamAccountID); teamAccountID != "" {
			resolvedTeamAccountID = teamAccountID
		}
		chatGPTUserID = strings.TrimSpace(workspaceInfo.ChatGPTUserID)
		teamName = strings.TrimSpace(workspaceInfo.TeamName)
		isTeamWorkspace = workspaceInfo.IsTeamWorkspace
		isTeamAdmin = workspaceInfo.IsTeamAdmin
	}

	if effectivePlan == "" {
		effectivePlan = "free"
	}

	ownerAccountID := input.TeamOwnerCredentialID
	if ownerAccountID <= 0 && resolvedTeamAccountID != "" && !isTeamAdmin {
		if owner, err := s.resolveTeamOwnerAccount(ctx, 0, resolvedTeamAccountID); err == nil && owner != nil {
			ownerAccountID = owner.ID
		}
	}

	tokenInfo := &OpenAITokenInfo{
		AccessToken:      accessToken,
		RefreshToken:     refreshToken,
		Email:            resolvedEmail,
		ChatGPTAccountID: resolvedAccountID,
		ChatGPTUserID:    chatGPTUserID,
		PlanType:         effectivePlan,
	}
	credentials := s.buildCodexCredentials(tokenInfo)
	extra := map[string]any{
		"codex_external_managed": true,
		"codex_external_public":  input.IsPublic,
		"codex_external_source":  strings.TrimSpace(input.Source),
		"effective_plan_type":    effectivePlan,
		"is_team_workspace":      isTeamWorkspace,
		"is_team_admin":          isTeamAdmin,
	}
	if resolvedTeamAccountID != "" {
		extra["team_account_id"] = resolvedTeamAccountID
	}
	if teamName != "" {
		extra["team_name"] = teamName
	}
	if ownerAccountID > 0 {
		extra["team_owner_account_id"] = ownerAccountID
		extra["team_owner_credential_id"] = ownerAccountID
	}
	if chatGPTUserID != "" && !isTeamAdmin {
		extra["team_member_user_id"] = chatGPTUserID
	}

	isValid := resolveErr == nil
	verifyMessage := ""
	if resolveErr != nil {
		verifyMessage = resolveErr.Error()
	}

	existing, err := s.findExistingCodexAccount(ctx, resolvedEmail, resolvedAccountID, chatGPTUserID)
	if err != nil {
		return nil, err
	}

	isNew := existing == nil
	var account *Account
	if existing == nil {
		account, err = s.adminService.CreateAccount(ctx, &CreateAccountInput{
			Name:        buildCodexAccountName(resolvedEmail, effectivePlan, resolvedAccountID),
			Platform:    PlatformOpenAI,
			Type:        AccountTypeOAuth,
			Credentials: credentials,
			Extra:       extra,
			Concurrency: 1,
			Priority:    0,
		})
		if err != nil {
			return nil, err
		}
	} else {
		account = existing
		account.Credentials = mergeStringAnyMap(account.Credentials, credentials)
		account.Extra = mergeStringAnyMap(account.Extra, extra)
		if resolvedEmail != "" || resolvedAccountID != "" {
			account.Name = buildCodexAccountName(resolvedEmail, effectivePlan, resolvedAccountID)
		}
		if err := s.accountRepo.Update(ctx, account); err != nil {
			return nil, err
		}
	}

	if isValid {
		_ = s.accountRepo.SetSchedulable(ctx, account.ID, true)
		_ = s.accountRepo.ClearError(ctx, account.ID)
	} else {
		_ = s.accountRepo.SetSchedulable(ctx, account.ID, false)
		_ = s.accountRepo.SetError(ctx, account.ID, truncateCodexExternalError(verifyMessage))
	}

	teamAdminName := ""
	if isTeamAdmin {
		teamAdminName = account.Name
	} else if ownerAccountID > 0 {
		if owner, ownerErr := s.accountRepo.GetByID(ctx, ownerAccountID); ownerErr == nil && owner != nil {
			teamAdminName = owner.Name
		}
	}

	return map[string]any{
		"success":          true,
		"message":          codexUpsertMessage(input.Source, isNew),
		"email":            resolvedEmail,
		"plan_type":        effectivePlan,
		"is_team_admin":    isTeamAdmin,
		"team_account_id":  resolvedTeamAccountID,
		"team_admin_name":  teamAdminName,
		"is_public":        input.IsPublic,
		"is_valid":         isValid,
		"is_new":           isNew,
		"reward_quota":     0,
		"account_id_hash":  shortStringHash(resolvedAccountID),
		"owner_account_id": ownerAccountID,
	}, nil
}

func (s *CodexExternalService) buildCodexCredentials(tokenInfo *OpenAITokenInfo) map[string]any {
	if s != nil && s.openaiOAuthService != nil {
		return s.openaiOAuthService.BuildAccountCredentials(tokenInfo)
	}
	credentials := map[string]any{
		"access_token":  tokenInfo.AccessToken,
		"refresh_token": tokenInfo.RefreshToken,
		"expires_at":    time.Now().Add(30 * 24 * time.Hour).Format(time.RFC3339),
	}
	if strings.TrimSpace(tokenInfo.Email) != "" {
		credentials["email"] = strings.TrimSpace(tokenInfo.Email)
	}
	if strings.TrimSpace(tokenInfo.ChatGPTAccountID) != "" {
		credentials["chatgpt_account_id"] = strings.TrimSpace(tokenInfo.ChatGPTAccountID)
	}
	if strings.TrimSpace(tokenInfo.ChatGPTUserID) != "" {
		credentials["chatgpt_user_id"] = strings.TrimSpace(tokenInfo.ChatGPTUserID)
	}
	if strings.TrimSpace(tokenInfo.PlanType) != "" {
		credentials["plan_type"] = strings.TrimSpace(tokenInfo.PlanType)
	}
	return credentials
}

func (s *CodexExternalService) listCodexAccounts(ctx context.Context) ([]Account, error) {
	accounts, err := s.accountRepo.ListByPlatform(ctx, PlatformOpenAI)
	if err != nil {
		return nil, err
	}
	filtered := make([]Account, 0, len(accounts))
	for _, account := range accounts {
		if !account.IsOpenAIOAuth() {
			continue
		}
		filtered = append(filtered, account)
	}
	return filtered, nil
}

func (s *CodexExternalService) findExistingCodexAccount(ctx context.Context, email, accountID, chatGPTUserID string) (*Account, error) {
	accounts, err := s.listCodexAccounts(ctx)
	if err != nil {
		return nil, err
	}
	normalizedEmail := strings.ToLower(strings.TrimSpace(email))
	accountID = strings.TrimSpace(accountID)
	chatGPTUserID = strings.TrimSpace(chatGPTUserID)
	for i := range accounts {
		account := &accounts[i]
		if accountID != "" && strings.TrimSpace(account.GetChatGPTAccountID()) == accountID {
			return account, nil
		}
		if chatGPTUserID != "" {
			if strings.TrimSpace(account.GetChatGPTUserID()) == chatGPTUserID ||
				strings.TrimSpace(account.GetExtraString("team_member_user_id")) == chatGPTUserID {
				return account, nil
			}
		}
		if normalizedEmail != "" && strings.EqualFold(strings.TrimSpace(account.GetCredential("email")), normalizedEmail) {
			return account, nil
		}
	}
	return nil, nil
}

func (s *CodexExternalService) resolveTeamOwnerAccount(ctx context.Context, ownerCredentialID int64, teamAccountID string) (*Account, error) {
	if s == nil || s.accountRepo == nil {
		return nil, infraerrors.New(http.StatusServiceUnavailable, "CODEX_EXTERNAL_UNAVAILABLE", "账号仓库不可用")
	}

	resolvedTeamAccountID := strings.TrimSpace(teamAccountID)
	if ownerCredentialID > 0 {
		owner, err := s.accountRepo.GetByID(ctx, ownerCredentialID)
		if err != nil {
			return nil, err
		}
		if owner == nil || !owner.IsOpenAIOAuth() {
			return nil, infraerrors.New(http.StatusNotFound, "CODEX_TEAM_OWNER_NOT_FOUND", "指定 Team 管理员凭证不存在")
		}
		if !isCodexTeamAdminAccount(owner) {
			return nil, infraerrors.New(http.StatusBadRequest, "CODEX_TEAM_OWNER_INVALID", "指定账号不是 Team 管理员凭证")
		}
		if resolvedTeamAccountID != "" && getCodexTeamAccountID(owner) != resolvedTeamAccountID {
			return nil, infraerrors.New(http.StatusBadRequest, "CODEX_TEAM_ACCOUNT_MISMATCH", "指定 Team 管理员与 team_account_id 不匹配")
		}
		return owner, nil
	}

	accounts, err := s.listCodexAccounts(ctx)
	if err != nil {
		return nil, err
	}

	candidates := make([]*Account, 0)
	for i := range accounts {
		account := &accounts[i]
		if !isCodexTeamAdminAccount(account) {
			continue
		}
		if resolvedTeamAccountID != "" && getCodexTeamAccountID(account) != resolvedTeamAccountID {
			continue
		}
		candidates = append(candidates, account)
	}
	if len(candidates) == 0 {
		return nil, infraerrors.New(http.StatusNotFound, "CODEX_TEAM_OWNER_NOT_FOUND", "未找到对应 Team 管理员凭证")
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		leftActive := candidates[i].IsActive()
		rightActive := candidates[j].IsActive()
		if leftActive != rightActive {
			return leftActive
		}
		leftSched := candidates[i].IsSchedulable()
		rightSched := candidates[j].IsSchedulable()
		if leftSched != rightSched {
			return leftSched
		}
		return candidates[i].ID < candidates[j].ID
	})
	return candidates[0], nil
}

func (s *CodexExternalService) collectTeamOwnerSnapshots(ctx context.Context) ([]teamOwnerSnapshot, error) {
	if s == nil || s.teamService == nil {
		return nil, infraerrors.New(http.StatusServiceUnavailable, "CODEX_TEAM_SERVICE_UNAVAILABLE", "Team 服务不可用")
	}

	accounts, err := s.listCodexAccounts(ctx)
	if err != nil {
		return nil, err
	}

	snapshots := make([]teamOwnerSnapshot, 0)
	for i := range accounts {
		account := &accounts[i]
		if !isCodexTeamAdminAccount(account) {
			continue
		}
		snapshot, err := s.teamService.GetTeamSnapshot(ctx, account, false)
		if err != nil {
			continue
		}
		snapshots = append(snapshots, teamOwnerSnapshot{
			Owner:    account,
			Snapshot: snapshot,
		})
	}
	return snapshots, nil
}

func (s *CodexExternalService) collectTeamPoolStatus(ctx context.Context) (map[string]any, error) {
	if s == nil || s.teamService == nil {
		return map[string]any{
			"teams":                 []map[string]any{},
			"team_count":            0,
			"total_available_slots": 0,
			"total_active_members":  0,
			"total_pending_invites": 0,
		}, nil
	}

	accounts, err := s.listCodexAccounts(ctx)
	if err != nil {
		return nil, err
	}

	teams := make([]map[string]any, 0)
	totalAvailableSlots := 0
	totalActiveMembers := 0
	totalPendingInvites := 0

	for i := range accounts {
		owner := &accounts[i]
		if !isCodexTeamAdminAccount(owner) {
			continue
		}

		teamAccountID := getCodexTeamAccountID(owner)
		memberLimit := defaultCodexTeamMemberLimit
		if owner.Extra != nil {
			if override := ParseExtraInt(owner.Extra["team_member_limit"]); override > 0 {
				memberLimit = override
			}
		}

		item := map[string]any{
			"owner_credential_id": owner.ID,
			"owner_email":         strings.TrimSpace(owner.GetCredential("email")),
			"owner_active":        owner.IsActive(),
			"team_account_id":     teamAccountID,
			"team_name":           strings.TrimSpace(owner.GetExtraString("team_name")),
			"member_limit":        memberLimit,
			"active_members":      0,
			"pending_invites":     0,
			"available_slots":     0,
			"members":             []ChatGPTTeamMember{},
			"invites":             []ChatGPTTeamInvite{},
			"error":               "",
		}

		if teamAccountID == "" {
			item["error"] = "missing_team_account_id"
			teams = append(teams, item)
			continue
		}
		if !owner.IsActive() {
			item["error"] = "owner_inactive"
			teams = append(teams, item)
			continue
		}

		snapshot, err := s.teamService.GetTeamSnapshot(ctx, owner, true)
		if err != nil {
			item["error"] = truncateCodexExternalError(err.Error())
			teams = append(teams, item)
			continue
		}

		if snapshot.MemberLimit > 0 {
			memberLimit = snapshot.MemberLimit
			item["member_limit"] = memberLimit
		}
		if strings.TrimSpace(snapshot.TeamName) != "" {
			item["team_name"] = strings.TrimSpace(snapshot.TeamName)
		}

		ownerEmail := strings.ToLower(strings.TrimSpace(snapshot.OwnerEmail))
		memberUsers := make([]ChatGPTTeamMember, 0, len(snapshot.Members))
		for _, member := range snapshot.Members {
			if ownerEmail != "" && strings.EqualFold(strings.TrimSpace(member.Email), ownerEmail) {
				continue
			}
			memberUsers = append(memberUsers, member)
		}
		activeMembers := len(memberUsers)
		pendingInvites := len(snapshot.Invites)
		availableSlots := memberLimit - activeMembers - pendingInvites
		if availableSlots < 0 {
			availableSlots = 0
		}

		item["active_members"] = activeMembers
		item["pending_invites"] = pendingInvites
		item["available_slots"] = availableSlots
		item["members"] = memberUsers
		item["invites"] = snapshot.Invites

		totalAvailableSlots += availableSlots
		totalActiveMembers += activeMembers
		totalPendingInvites += pendingInvites
		teams = append(teams, item)
	}

	return map[string]any{
		"teams":                 teams,
		"team_count":            len(teams),
		"total_available_slots": totalAvailableSlots,
		"total_active_members":  totalActiveMembers,
		"total_pending_invites": totalPendingInvites,
	}, nil
}

func (s *CodexExternalService) findLocalTeamMemberAccount(ctx context.Context, email, teamMemberUserID, teamAccountID string) *Account {
	accounts, err := s.listCodexAccounts(ctx)
	if err != nil {
		return nil
	}

	normalizedEmail := strings.ToLower(strings.TrimSpace(email))
	normalizedUserID := strings.TrimSpace(teamMemberUserID)
	normalizedTeamAccountID := strings.TrimSpace(teamAccountID)

	for i := range accounts {
		account := &accounts[i]
		if isCodexTeamAdminAccount(account) {
			continue
		}
		if normalizedTeamAccountID != "" && getCodexTeamAccountID(account) != normalizedTeamAccountID {
			continue
		}
		if normalizedUserID != "" {
			if strings.TrimSpace(account.GetChatGPTUserID()) == normalizedUserID ||
				strings.TrimSpace(account.GetExtraString("team_member_user_id")) == normalizedUserID {
				return account
			}
		}
		if normalizedEmail != "" && strings.EqualFold(strings.TrimSpace(account.GetCredential("email")), normalizedEmail) {
			return account
		}
	}
	return nil
}

func (s *CodexExternalService) deleteLocalCodexAccount(ctx context.Context, account *Account, reason string) error {
	if s == nil || s.accountRepo == nil {
		return infraerrors.New(http.StatusServiceUnavailable, "CODEX_EXTERNAL_UNAVAILABLE", "账号仓库不可用")
	}
	if account == nil {
		return infraerrors.New(http.StatusBadRequest, "CODEX_ACCOUNT_REQUIRED", "account 不能为空")
	}

	_ = s.accountRepo.SetSchedulable(ctx, account.ID, false)
	if err := s.accountRepo.Delete(ctx, account.ID); err != nil {
		_ = s.accountRepo.SetError(ctx, account.ID, truncateCodexExternalError("AUTO_DELETE_FAILED: "+reason+": "+err.Error()))
		return err
	}
	return nil
}

func buildExternalCodexStatusPayload(accounts []Account, teamStatus map[string]any) map[string]any {
	summary := map[string]int{
		"total_credentials":         0,
		"active_credentials":        0,
		"public_credentials":        0,
		"public_active_credentials": 0,
		"team_admin_total":          0,
		"team_admin_active":         0,
	}
	byPlan := map[string]map[string]int{
		"free": {"total": 0, "active": 0, "public": 0, "public_active": 0},
		"plus": {"total": 0, "active": 0, "public": 0, "public_active": 0},
		"pro":  {"total": 0, "active": 0, "public": 0, "public_active": 0},
		"team": {"total": 0, "active": 0, "public": 0, "public_active": 0},
	}

	credentialEmailSet := make(map[string]struct{})
	for i := range accounts {
		account := &accounts[i]
		plan := normalizeCodexPlanType(account.GetCredential("plan_type"))
		if plan == "free" {
			if extraPlan := normalizeCodexPlanType(account.GetExtraString("effective_plan_type")); extraPlan != "" {
				plan = extraPlan
			}
		}
		bucket, ok := byPlan[plan]
		if !ok {
			bucket = map[string]int{"total": 0, "active": 0, "public": 0, "public_active": 0}
			byPlan[plan] = bucket
		}

		isActive := account.IsActive()
		isPublic := isCodexExternalPublic(account)
		isTeamAdmin := isCodexTeamAdminAccount(account)

		summary["total_credentials"]++
		bucket["total"]++
		if isActive {
			summary["active_credentials"]++
			bucket["active"]++
		}
		if isPublic {
			summary["public_credentials"]++
			bucket["public"]++
		}
		if isActive && isPublic {
			summary["public_active_credentials"]++
			bucket["public_active"]++
		}
		if isTeamAdmin {
			summary["team_admin_total"]++
			if isActive {
				summary["team_admin_active"]++
			}
		}

		email := strings.ToLower(strings.TrimSpace(account.GetCredential("email")))
		if email != "" {
			credentialEmailSet[email] = struct{}{}
		}
	}

	credentialEmails := make([]string, 0, len(credentialEmailSet))
	for email := range credentialEmailSet {
		credentialEmails = append(credentialEmails, email)
	}
	sort.Strings(credentialEmails)

	teamPool := map[string]any{
		"team_count":            0,
		"total_available_slots": 0,
		"total_active_members":  0,
		"total_pending_invites": 0,
		"teams":                 []map[string]any{},
	}
	if teamStatus != nil {
		teamPool["team_count"] = ParseExtraInt(teamStatus["team_count"])
		teamPool["total_available_slots"] = ParseExtraInt(teamStatus["total_available_slots"])
		teamPool["total_active_members"] = ParseExtraInt(teamStatus["total_active_members"])
		teamPool["total_pending_invites"] = ParseExtraInt(teamStatus["total_pending_invites"])
		if rawTeams, ok := teamStatus["teams"].([]map[string]any); ok {
			sanitizedTeams := make([]map[string]any, 0, len(rawTeams))
			for _, item := range rawTeams {
				sanitizedTeams = append(sanitizedTeams, sanitizeExternalCodexTeamPoolItem(item))
			}
			teamPool["teams"] = sanitizedTeams
		} else if rawTeams, ok := teamStatus["teams"].([]any); ok {
			sanitizedTeams := make([]map[string]any, 0, len(rawTeams))
			for _, raw := range rawTeams {
				item, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				sanitizedTeams = append(sanitizedTeams, sanitizeExternalCodexTeamPoolItem(item))
			}
			teamPool["teams"] = sanitizedTeams
		}
	}

	return map[string]any{
		"summary": map[string]any{
			"total_credentials":         summary["total_credentials"],
			"active_credentials":        summary["active_credentials"],
			"public_credentials":        summary["public_credentials"],
			"public_active_credentials": summary["public_active_credentials"],
			"team_admin_total":          summary["team_admin_total"],
			"team_admin_active":         summary["team_admin_active"],
			"free_public_active":        byPlan["free"]["public_active"],
			"plus_public_active":        byPlan["plus"]["public_active"],
			"pro_public_active":         byPlan["pro"]["public_active"],
			"team_public_active":        byPlan["team"]["public_active"],
		},
		"by_plan":           byPlan,
		"credential_emails": credentialEmails,
		"team_pool":         teamPool,
	}
}

func sanitizeExternalCodexTeamPoolItem(item map[string]any) map[string]any {
	return map[string]any{
		"owner_credential_id": ParseExtraInt(item["owner_credential_id"]),
		"owner_email":         strings.TrimSpace(fmt.Sprint(item["owner_email"])),
		"owner_active":        resolveAnyBool(item["owner_active"]),
		"team_account_id":     strings.TrimSpace(fmt.Sprint(item["team_account_id"])),
		"team_name":           strings.TrimSpace(fmt.Sprint(item["team_name"])),
		"member_limit":        ParseExtraInt(item["member_limit"]),
		"active_members":      ParseExtraInt(item["active_members"]),
		"pending_invites":     ParseExtraInt(item["pending_invites"]),
		"available_slots":     ParseExtraInt(item["available_slots"]),
		"error":               strings.TrimSpace(fmt.Sprint(item["error"])),
	}
}

func buildCodexAccountName(email, planType, accountID string) string {
	plan := normalizeCodexPlanType(planType)
	normalizedEmail := strings.ToLower(strings.TrimSpace(email))
	switch {
	case normalizedEmail != "":
		return fmt.Sprintf("codex-%s-%s", plan, normalizedEmail)
	case strings.TrimSpace(accountID) != "":
		return fmt.Sprintf("codex-%s-%s", plan, shortStringHash(accountID))
	default:
		return fmt.Sprintf("codex-%s", plan)
	}
}

func shortStringHash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:])[:8]
}

func codexUpsertMessage(source string, isNew bool) string {
	switch strings.TrimSpace(source) {
	case "callback":
		if isNew {
			return "凭证授权成功"
		}
		return "凭证已更新"
	default:
		if isNew {
			return "凭证推送成功"
		}
		return "凭证已更新"
	}
}

func mergeStringAnyMap(base map[string]any, patch map[string]any) map[string]any {
	if len(base) == 0 && len(patch) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(base)+len(patch))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range patch {
		out[key] = value
	}
	return out
}

func normalizeEmailList(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, raw := range values {
		email := strings.ToLower(strings.TrimSpace(raw))
		if email == "" {
			continue
		}
		if _, ok := seen[email]; ok {
			continue
		}
		seen[email] = struct{}{}
		out = append(out, email)
	}
	return out
}

func hasInviteFailure(results []map[string]any) bool {
	for _, item := range results {
		if success, _ := item["success"].(bool); !success {
			return true
		}
	}
	return false
}

func truncateCodexExternalError(message string) string {
	text := strings.TrimSpace(message)
	if text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= 450 {
		return text
	}
	return string(runes[:450])
}

func isCodexExternalPublic(account *Account) bool {
	if account == nil || account.Extra == nil {
		return false
	}
	return resolveAnyBool(account.Extra["codex_external_public"])
}

func isCodexTeamAdminAccount(account *Account) bool {
	if account == nil || account.Extra == nil {
		return false
	}
	return resolveAnyBool(account.Extra["is_team_admin"])
}

func resolveAnyBool(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "y", "on":
			return true
		}
	case int:
		return v != 0
	case int64:
		return v != 0
	case float64:
		return v != 0
	}
	return false
}
