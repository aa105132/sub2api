package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"github.com/tidwall/gjson"
)

const (
	defaultCodexTeamMemberLimit = 5
)

var (
	chatGPTBackendAPIBase = "https://chatgpt.com/backend-api"
	chatGPTAccountsCheck  = chatGPTBackendAPIBase + "/accounts/check/v4-2023-04-27"
	chatGPTWebClientUA    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36"
)

var openAIChatGPTTeamPlanTypes = map[string]struct{}{
	"team":       {},
	"enterprise": {},
	"business":   {},
}

var openAIChatGPTStandardMemberRoles = map[string]struct{}{
	"standard-user": {},
	"standard_user": {},
	"member":        {},
	"user":          {},
}

// ChatGPTWorkspaceInfo describes resolved Codex workspace metadata.
type ChatGPTWorkspaceInfo struct {
	EffectivePlanType string `json:"effective_plan_type"`
	AccountID         string `json:"account_id,omitempty"`
	Email             string `json:"email,omitempty"`
	ChatGPTUserID     string `json:"chatgpt_user_id,omitempty"`
	IsTeamWorkspace   bool   `json:"is_team_workspace"`
	IsTeamAdmin       bool   `json:"is_team_admin"`
	TeamName          string `json:"team_name,omitempty"`
	TeamAccountID     string `json:"team_account_id,omitempty"`
	ManagedTeamCount  int    `json:"managed_team_count,omitempty"`
}

// ChatGPTTeamMember describes a team member entry.
type ChatGPTTeamMember struct {
	UserID string `json:"user_id,omitempty"`
	Email  string `json:"email,omitempty"`
	Role   string `json:"role,omitempty"`
}

// ChatGPTTeamInvite describes a pending invite entry.
type ChatGPTTeamInvite struct {
	Email string `json:"email,omitempty"`
}

// ChatGPTTeamSnapshot describes a team state snapshot.
type ChatGPTTeamSnapshot struct {
	OwnerAccountID int64               `json:"owner_account_id"`
	OwnerName      string              `json:"owner_name,omitempty"`
	OwnerEmail     string              `json:"owner_email,omitempty"`
	TeamAccountID  string              `json:"team_account_id,omitempty"`
	TeamName       string              `json:"team_name,omitempty"`
	IsTeamAdmin    bool                `json:"is_team_admin"`
	MemberLimit    int                 `json:"member_limit"`
	MemberCount    int                 `json:"member_count"`
	InviteCount    int                 `json:"invite_count"`
	Vacancies      int                 `json:"vacancies"`
	Members        []ChatGPTTeamMember `json:"members,omitempty"`
	Invites        []ChatGPTTeamInvite `json:"invites,omitempty"`
}

// OpenAIChatGPTTeamService provides ChatGPT workspace and team management.
type OpenAIChatGPTTeamService struct {
	accountRepo        AccountRepository
	openaiOAuthService *OpenAIOAuthService
}

// NewOpenAIChatGPTTeamService creates a new ChatGPT team service.
func NewOpenAIChatGPTTeamService(accountRepo AccountRepository, openaiOAuthService *OpenAIOAuthService) *OpenAIChatGPTTeamService {
	return &OpenAIChatGPTTeamService{
		accountRepo:        accountRepo,
		openaiOAuthService: openaiOAuthService,
	}
}

// ResolveWorkspaceInfo resolves plan and team metadata from ChatGPT backend.
func (s *OpenAIChatGPTTeamService) ResolveWorkspaceInfo(
	ctx context.Context,
	accessToken string,
	fallbackPlanType string,
	accountID string,
	email string,
	hintedTeamAccountID string,
) (*ChatGPTWorkspaceInfo, error) {
	info := &ChatGPTWorkspaceInfo{
		EffectivePlanType: normalizeCodexPlanType(fallbackPlanType),
		AccountID:         strings.TrimSpace(accountID),
		Email:             strings.TrimSpace(email),
		TeamAccountID:     strings.TrimSpace(hintedTeamAccountID),
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, chatGPTAccountsCheck, nil)
	if err != nil {
		return info, err
	}
	s.applyChatGPTCommonHeaders(req, accessToken, accountID)

	client, err := s.getHTTPClient(nil)
	if err != nil {
		return info, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return info, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode >= 400 {
		return info, fmt.Errorf("accounts/check failed: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	if chatGPTUserID := strings.TrimSpace(gjson.GetBytes(body, "user.id").String()); chatGPTUserID != "" {
		info.ChatGPTUserID = chatGPTUserID
	}
	if info.Email == "" {
		if resolvedEmail := strings.TrimSpace(gjson.GetBytes(body, "user.email").String()); resolvedEmail != "" {
			info.Email = resolvedEmail
		}
	}

	accounts := gjson.GetBytes(body, "accounts").Array()
	if len(accounts) == 0 {
		return info, nil
	}

	selectedAccountID := info.AccountID
	if selectedAccountID == "" {
		selectedAccountID = strings.TrimSpace(accounts[0].Get("account_id").String())
	}

	for _, account := range accounts {
		currentAccountID := strings.TrimSpace(account.Get("account_id").String())
		planType := normalizeCodexPlanType(account.Get("account.plan_type").String())
		if planType == "" {
			planType = normalizeCodexPlanType(account.Get("plan_type").String())
		}
		if currentAccountID == selectedAccountID && planType != "" {
			info.EffectivePlanType = planType
		}

		if _, ok := openAIChatGPTTeamPlanTypes[planType]; !ok {
			continue
		}

		info.IsTeamWorkspace = true
		info.ManagedTeamCount++

		candidateTeamID := currentAccountID
		if candidateTeamID == "" {
			candidateTeamID = strings.TrimSpace(account.Get("id").String())
		}
		candidateTeamName := strings.TrimSpace(account.Get("account.name").String())
		if candidateTeamName == "" {
			candidateTeamName = strings.TrimSpace(account.Get("name").String())
		}

		isPreferred := hintedTeamAccountID != "" && candidateTeamID == info.TeamAccountID
		if info.TeamAccountID == "" || isPreferred {
			info.TeamAccountID = candidateTeamID
			info.TeamName = candidateTeamName
		}

		memberRole, roleErr := s.resolveCurrentMemberRole(ctx, accessToken, candidateTeamID, info.Email, info.ChatGPTUserID)
		if roleErr != nil {
			continue
		}
		if _, ok := openAIChatGPTStandardMemberRoles[strings.ToLower(strings.TrimSpace(memberRole))]; ok {
			continue
		}

		info.IsTeamAdmin = true
		info.EffectivePlanType = "team"
		info.TeamAccountID = candidateTeamID
		info.TeamName = candidateTeamName

		if hintedTeamAccountID == "" || candidateTeamID == hintedTeamAccountID {
			break
		}
	}

	if info.EffectivePlanType == "" {
		info.EffectivePlanType = normalizeCodexPlanType(fallbackPlanType)
	}
	return info, nil
}

// GetTeamSnapshot loads users/invites for a team admin account.
func (s *OpenAIChatGPTTeamService) GetTeamSnapshot(ctx context.Context, owner *Account, includeMembers bool) (*ChatGPTTeamSnapshot, error) {
	if owner == nil {
		return nil, fmt.Errorf("owner account is nil")
	}

	teamAccountID := getCodexTeamAccountID(owner)
	if teamAccountID == "" {
		return nil, fmt.Errorf("team_account_id is empty")
	}

	membersPayload, err := s.requestTeamAPIWithRetry(ctx, owner, http.MethodGet, teamAccountID, "/users?limit=100&offset=0", nil)
	if err != nil {
		return nil, err
	}
	invitesPayload, err := s.requestTeamAPIWithRetry(ctx, owner, http.MethodGet, teamAccountID, "/invites", nil)
	if err != nil {
		return nil, err
	}

	members := extractTeamMembers(membersPayload)
	invites := extractTeamInvites(invitesPayload)
	memberLimit := defaultCodexTeamMemberLimit
	if owner != nil {
		if override := ParseExtraInt(owner.Extra["team_member_limit"]); override > 0 {
			memberLimit = override
		}
	}
	vacancies := memberLimit - len(members) - len(invites)
	if vacancies < 0 {
		vacancies = 0
	}

	snapshot := &ChatGPTTeamSnapshot{
		OwnerAccountID: owner.ID,
		OwnerName:      owner.Name,
		OwnerEmail:     strings.TrimSpace(owner.GetCredential("email")),
		TeamAccountID:  teamAccountID,
		TeamName:       strings.TrimSpace(owner.GetExtraString("team_name")),
		IsTeamAdmin:    owner.Extra != nil && owner.Extra["is_team_admin"] == true,
		MemberLimit:    memberLimit,
		MemberCount:    len(members),
		InviteCount:    len(invites),
		Vacancies:      vacancies,
	}
	if includeMembers {
		snapshot.Members = members
		snapshot.Invites = invites
	}
	return snapshot, nil
}

// InviteMembers sends team invites via the owner account.
func (s *OpenAIChatGPTTeamService) InviteMembers(ctx context.Context, owner *Account, emails []string) error {
	if owner == nil {
		return fmt.Errorf("owner account is nil")
	}
	normalized := make([]string, 0, len(emails))
	for _, email := range emails {
		email = strings.ToLower(strings.TrimSpace(email))
		if email != "" {
			normalized = append(normalized, email)
		}
	}
	if len(normalized) == 0 {
		return fmt.Errorf("emails are empty")
	}

	payload := map[string]any{
		"email_addresses": normalized,
		"role":            "standard-user",
		"resend_emails":   true,
	}
	_, err := s.requestTeamAPIWithRetry(ctx, owner, http.MethodPost, getCodexTeamAccountID(owner), "/invites", payload)
	return err
}

// RemoveMemberOrInvite removes a team member or pending invite by user id/email.
func (s *OpenAIChatGPTTeamService) RemoveMemberOrInvite(
	ctx context.Context,
	owner *Account,
	teamAccountID string,
	teamMemberUserID string,
	email string,
) (string, error) {
	if owner == nil {
		return "", fmt.Errorf("owner account is nil")
	}
	if strings.TrimSpace(teamAccountID) == "" {
		teamAccountID = getCodexTeamAccountID(owner)
	}
	if strings.TrimSpace(teamAccountID) == "" {
		return "", fmt.Errorf("team_account_id is empty")
	}

	normalizedEmail := strings.ToLower(strings.TrimSpace(email))
	if strings.TrimSpace(teamMemberUserID) == "" && normalizedEmail != "" {
		snapshot, err := s.GetTeamSnapshot(ctx, owner, true)
		if err == nil {
			for _, member := range snapshot.Members {
				if strings.EqualFold(member.Email, normalizedEmail) {
					teamMemberUserID = member.UserID
					break
				}
			}
		}
	}

	if strings.TrimSpace(teamMemberUserID) != "" {
		_, err := s.requestTeamAPIWithRetry(ctx, owner, http.MethodDelete, teamAccountID, "/users/"+url.PathEscape(strings.TrimSpace(teamMemberUserID)), nil)
		if err != nil {
			return "", err
		}
		return "removed_member", nil
	}

	if normalizedEmail == "" {
		return "", fmt.Errorf("email is empty")
	}
	payload := map[string]any{"email_address": normalizedEmail}
	_, err := s.requestTeamAPIWithRetry(ctx, owner, http.MethodDelete, teamAccountID, "/invites", payload)
	if err != nil {
		return "", err
	}
	return "removed_invite", nil
}

func (s *OpenAIChatGPTTeamService) resolveCurrentMemberRole(ctx context.Context, accessToken, teamAccountID, email, chatGPTUserID string) (string, error) {
	payload, err := s.requestTeamAPIByToken(ctx, accessToken, teamAccountID, http.MethodGet, "/users?limit=100&offset=0", nil)
	if err != nil {
		return "", err
	}

	normalizedEmail := strings.ToLower(strings.TrimSpace(email))
	for _, item := range gjson.ParseBytes(payload).Get("users").Array() {
		memberEmail := strings.ToLower(strings.TrimSpace(extractTeamItemEmail(item)))
		memberUserID := strings.TrimSpace(extractTeamItemUserID(item))
		role := strings.TrimSpace(item.Get("role").String())
		if role == "" {
			role = strings.TrimSpace(item.Get("user.role").String())
		}
		if normalizedEmail != "" && memberEmail == normalizedEmail {
			return role, nil
		}
		if chatGPTUserID != "" && memberUserID == chatGPTUserID {
			return role, nil
		}
	}
	return "", fmt.Errorf("member role not found")
}

func (s *OpenAIChatGPTTeamService) requestTeamAPIWithRetry(
	ctx context.Context,
	owner *Account,
	method string,
	teamAccountID string,
	path string,
	payload any,
) ([]byte, error) {
	if owner == nil {
		return nil, fmt.Errorf("owner account is nil")
	}
	accessToken := owner.GetOpenAIAccessToken()
	body, statusCode, err := s.requestTeamAPIByAccount(ctx, owner, accessToken, teamAccountID, method, path, payload)
	if err == nil {
		return body, nil
	}
	if statusCode != http.StatusUnauthorized && statusCode != http.StatusForbidden {
		return nil, err
	}

	refreshedToken, refreshErr := s.refreshOpenAIOAuthAccount(ctx, owner)
	if refreshErr != nil {
		return nil, err
	}
	body, _, err = s.requestTeamAPIByAccount(ctx, owner, refreshedToken, teamAccountID, method, path, payload)
	return body, err
}

func (s *OpenAIChatGPTTeamService) requestTeamAPIByAccount(
	ctx context.Context,
	account *Account,
	accessToken string,
	teamAccountID string,
	method string,
	path string,
	payload any,
) ([]byte, int, error) {
	client, err := s.getHTTPClient(account)
	if err != nil {
		return nil, 0, err
	}
	reqURL := strings.TrimRight(chatGPTBackendAPIBase, "/") + "/accounts/" + url.PathEscape(strings.TrimSpace(teamAccountID)) + path
	var bodyReader io.Reader
	if payload != nil {
		raw, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return nil, 0, marshalErr
		}
		bodyReader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
	if err != nil {
		return nil, 0, err
	}
	s.applyChatGPTCommonHeaders(req, accessToken, account.GetChatGPTAccountID())
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode >= 400 {
		return body, resp.StatusCode, fmt.Errorf("team api failed: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, resp.StatusCode, nil
}

func (s *OpenAIChatGPTTeamService) requestTeamAPIByToken(
	ctx context.Context,
	accessToken string,
	teamAccountID string,
	method string,
	path string,
	payload any,
) ([]byte, error) {
	client, err := s.getHTTPClient(nil)
	if err != nil {
		return nil, err
	}
	reqURL := strings.TrimRight(chatGPTBackendAPIBase, "/") + "/accounts/" + url.PathEscape(strings.TrimSpace(teamAccountID)) + path
	var bodyReader io.Reader
	if payload != nil {
		raw, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return nil, marshalErr
		}
		bodyReader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
	if err != nil {
		return nil, err
	}
	s.applyChatGPTCommonHeaders(req, accessToken, "")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("team api failed: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func (s *OpenAIChatGPTTeamService) refreshOpenAIOAuthAccount(ctx context.Context, account *Account) (string, error) {
	if s == nil || s.openaiOAuthService == nil || account == nil {
		return "", fmt.Errorf("openai oauth refresh not available")
	}
	tokenInfo, err := s.openaiOAuthService.RefreshAccountToken(ctx, account)
	if err != nil {
		return "", err
	}
	newCredentials := s.openaiOAuthService.BuildAccountCredentials(tokenInfo)
	if account.Credentials == nil {
		account.Credentials = map[string]any{}
	}
	for key, value := range account.Credentials {
		if _, exists := newCredentials[key]; !exists {
			newCredentials[key] = value
		}
	}
	account.Credentials = newCredentials
	if s.accountRepo != nil {
		if updateErr := s.accountRepo.Update(ctx, account); updateErr != nil {
			return tokenInfo.AccessToken, updateErr
		}
	}
	return tokenInfo.AccessToken, nil
}

func (s *OpenAIChatGPTTeamService) getHTTPClient(account *Account) (*http.Client, error) {
	opts := httpclient.Options{
		Timeout:               30 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
	}
	if account != nil && account.Proxy != nil {
		opts.ProxyURL = account.Proxy.URL()
	}
	return httpclient.GetClient(opts)
}

func (s *OpenAIChatGPTTeamService) applyChatGPTCommonHeaders(req *http.Request, accessToken, chatGPTAccountID string) {
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Origin", "https://chatgpt.com")
	req.Header.Set("Referer", "https://chatgpt.com/")
	req.Header.Set("User-Agent", chatGPTWebClientUA)
	if strings.TrimSpace(chatGPTAccountID) != "" {
		req.Header.Set("Chatgpt-Account-Id", strings.TrimSpace(chatGPTAccountID))
	}
}

func getCodexTeamAccountID(account *Account) string {
	if account == nil {
		return ""
	}
	if value := strings.TrimSpace(account.GetExtraString("team_account_id")); value != "" {
		return value
	}
	return strings.TrimSpace(account.GetChatGPTAccountID())
}

func extractTeamMembers(payload []byte) []ChatGPTTeamMember {
	items := extractTeamItems(payload, "users")
	members := make([]ChatGPTTeamMember, 0, len(items))
	for _, item := range items {
		role := strings.TrimSpace(item.Get("role").String())
		if role == "" {
			role = strings.TrimSpace(item.Get("user.role").String())
		}
		members = append(members, ChatGPTTeamMember{
			UserID: strings.TrimSpace(extractTeamItemUserID(item)),
			Email:  strings.TrimSpace(extractTeamItemEmail(item)),
			Role:   role,
		})
	}
	return members
}

func extractTeamInvites(payload []byte) []ChatGPTTeamInvite {
	items := extractTeamItems(payload, "invites")
	invites := make([]ChatGPTTeamInvite, 0, len(items))
	for _, item := range items {
		email := strings.TrimSpace(extractTeamItemEmail(item))
		if email == "" {
			continue
		}
		invites = append(invites, ChatGPTTeamInvite{Email: email})
	}
	return invites
}

func extractTeamItems(payload []byte, fallbackKey string) []gjson.Result {
	root := gjson.ParseBytes(payload)
	for _, key := range []string{fallbackKey, "items", "data", "results"} {
		items := root.Get(key).Array()
		if len(items) > 0 {
			return items
		}
	}
	return nil
}

func extractTeamItemEmail(item gjson.Result) string {
	for _, path := range []string{"email", "email_address", "user.email", "user.email_address"} {
		if value := strings.TrimSpace(item.Get(path).String()); value != "" {
			return value
		}
	}
	return ""
}

func extractTeamItemUserID(item gjson.Result) string {
	for _, path := range []string{"id", "user_id", "member_id", "user.id", "user.user_id", "user.member_id"} {
		if value := strings.TrimSpace(item.Get(path).String()); value != "" {
			return value
		}
	}
	return ""
}
