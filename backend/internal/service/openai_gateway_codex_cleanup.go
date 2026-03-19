package service

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

func (s *OpenAIGatewayService) shouldAutoDeleteCodexCredential(account *Account, statusCode int) bool {
	if s == nil || account == nil {
		return false
	}
	if account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth {
		return false
	}
	switch statusCode {
	case http.StatusUnauthorized, http.StatusPaymentRequired:
		return true
	default:
		return false
	}
}

func (s *OpenAIGatewayService) handleCodexCredentialFailure(ctx context.Context, account *Account, statusCode int, respBody []byte) {
	if !s.shouldAutoDeleteCodexCredential(account, statusCode) || s.accountRepo == nil {
		return
	}

	_ = s.accountRepo.SetSchedulable(ctx, account.ID, false)

	if statusCode == http.StatusUnauthorized && s.teamService != nil &&
		isCodexTeamPlanType(account.GetCredential("plan_type")) &&
		!isCodexTeamAdminAccount(account) {
		if owner, err := s.resolveCodexTeamOwnerAccount(ctx, account); err == nil && owner != nil {
			_, _ = s.teamService.RemoveMemberOrInvite(
				ctx,
				owner,
				getCodexTeamAccountID(owner),
				strings.TrimSpace(account.GetExtraString("team_member_user_id")),
				strings.TrimSpace(account.GetCredential("email")),
			)
		}
	}

	if err := s.accountRepo.Delete(ctx, account.ID); err != nil {
		_ = s.accountRepo.SetError(ctx, account.ID, buildCodexAutoDeleteFailureMessage(statusCode, respBody, err))
	}
}

func (s *OpenAIGatewayService) resolveCodexTeamOwnerAccount(ctx context.Context, account *Account) (*Account, error) {
	if s == nil || s.accountRepo == nil || account == nil {
		return nil, fmt.Errorf("codex team owner resolve unavailable")
	}
	if isCodexTeamAdminAccount(account) || !isCodexTeamPlanType(account.GetCredential("plan_type")) {
		return nil, fmt.Errorf("not a team member account")
	}

	teamAccountID := getCodexTeamAccountID(account)
	if teamAccountID == "" {
		return nil, fmt.Errorf("team_account_id is empty")
	}

	ownerID := int64(ParseExtraInt(account.Extra["team_owner_account_id"]))
	if ownerID <= 0 {
		ownerID = int64(ParseExtraInt(account.Extra["team_owner_credential_id"]))
	}
	if ownerID > 0 {
		owner, err := s.accountRepo.GetByID(ctx, ownerID)
		if err == nil && owner != nil && isCodexTeamAdminAccount(owner) {
			return owner, nil
		}
	}

	accounts, err := s.accountRepo.ListByPlatform(ctx, PlatformOpenAI)
	if err != nil {
		return nil, err
	}
	for i := range accounts {
		owner := &accounts[i]
		if !owner.IsOpenAIOAuth() || !isCodexTeamAdminAccount(owner) {
			continue
		}
		if getCodexTeamAccountID(owner) == teamAccountID {
			return owner, nil
		}
	}
	return nil, fmt.Errorf("team owner not found")
}

func buildCodexAutoDeleteFailureMessage(statusCode int, respBody []byte, deleteErr error) string {
	reason := "上游错误"
	switch statusCode {
	case http.StatusUnauthorized:
		reason = "401 上游认证失败"
	case http.StatusPaymentRequired:
		reason = "402 上游计费失败"
	}

	upstreamMsg := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(respBody)))
	if upstreamMsg == "" {
		upstreamMsg = strings.TrimSpace(string(respBody))
	}

	message := fmt.Sprintf("AUTO_DELETE_%d: %s", statusCode, reason)
	if upstreamMsg != "" {
		message += ": " + truncateCodexExternalError(upstreamMsg)
	}
	if deleteErr != nil {
		message += ": delete_failed=" + truncateCodexExternalError(deleteErr.Error())
	}
	return truncateCodexExternalError(message)
}
