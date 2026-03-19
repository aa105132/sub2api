package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

type CustomEndpointModelSetting struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Platform string   `json:"platform"`
	BaseURL  string   `json:"base_url"`
	Models   []string `json:"models"`
}

func parseCustomEndpointModelSettings(raw string) []CustomEndpointModelSetting {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "[]" {
		return nil
	}

	var items []CustomEndpointModelSetting
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil
	}

	normalized, err := normalizeCustomEndpointModelSettings(items)
	if err != nil {
		return nil
	}
	return normalized
}

func normalizeCustomEndpointModelSettings(items []CustomEndpointModelSetting) ([]CustomEndpointModelSetting, error) {
	normalized := make([]CustomEndpointModelSetting, 0, len(items))
	for index, item := range items {
		if isEmptyCustomEndpointModelSetting(item) {
			continue
		}

		platform := normalizeCustomEndpointModelPlatform(item.Platform)
		if platform == "" {
			return nil, fmt.Errorf("custom endpoint model #%d 平台无效", index+1)
		}

		baseURL := normalizeCustomEndpointModelBaseURL(item.BaseURL)
		if baseURL == "" {
			return nil, fmt.Errorf("custom endpoint model #%d 端点地址无效", index+1)
		}

		models := normalizeCustomEndpointModelNames(item.Models)
		if len(models) == 0 {
			return nil, fmt.Errorf("custom endpoint model #%d 模型列表不能为空", index+1)
		}

		normalized = append(normalized, CustomEndpointModelSetting{
			ID:       strings.TrimSpace(item.ID),
			Name:     strings.TrimSpace(item.Name),
			Platform: platform,
			BaseURL:  baseURL,
			Models:   models,
		})
	}
	return normalized, nil
}

func isEmptyCustomEndpointModelSetting(item CustomEndpointModelSetting) bool {
	return strings.TrimSpace(item.ID) == "" &&
		strings.TrimSpace(item.Name) == "" &&
		strings.TrimSpace(item.Platform) == "" &&
		strings.TrimSpace(item.BaseURL) == "" &&
		len(item.Models) == 0
}

func normalizeCustomEndpointModelPlatform(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case PlatformAnthropic, "claude":
		return PlatformAnthropic
	case PlatformOpenAI:
		return PlatformOpenAI
	case PlatformGemini:
		return PlatformGemini
	case PlatformAntigravity:
		return PlatformAntigravity
	case PlatformSora:
		return PlatformSora
	default:
		return ""
	}
}

func normalizeCustomEndpointModelBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}

	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.Path = strings.TrimRight(parsed.EscapedPath(), "/")
	parsed.RawPath = ""

	return strings.TrimRight(parsed.String(), "/")
}

func normalizeCustomEndpointModelNames(models []string) []string {
	if len(models) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(models))
	normalized := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, exists := seen[model]; exists {
			continue
		}
		seen[model] = struct{}{}
		normalized = append(normalized, model)
	}
	return normalized
}

func matchCustomEndpointModels(items []CustomEndpointModelSetting, platform string, baseURL string) []string {
	platform = normalizeCustomEndpointModelPlatform(platform)
	baseURL = normalizeCustomEndpointModelBaseURL(baseURL)
	if platform == "" || baseURL == "" || len(items) == 0 {
		return nil
	}

	seen := make(map[string]struct{})
	matched := make([]string, 0)
	for _, item := range items {
		if item.Platform != platform || item.BaseURL != baseURL {
			continue
		}
		for _, model := range item.Models {
			if _, exists := seen[model]; exists {
				continue
			}
			seen[model] = struct{}{}
			matched = append(matched, model)
		}
	}
	return matched
}

func (s *SettingService) GetCustomEndpointModelSettings(ctx context.Context) []CustomEndpointModelSetting {
	if s == nil || s.settingRepo == nil {
		return nil
	}

	raw, err := s.settingRepo.GetValue(ctx, SettingKeyCustomEndpointModels)
	if err != nil {
		return nil
	}
	return parseCustomEndpointModelSettings(raw)
}

func (s *SettingService) GetCustomEndpointModels(ctx context.Context, platform string, baseURL string) []string {
	return matchCustomEndpointModels(s.GetCustomEndpointModelSettings(ctx), platform, baseURL)
}

func (s *SettingService) GetCustomEndpointModelsForAccount(ctx context.Context, account *Account) []string {
	if account == nil {
		return nil
	}

	baseURL := strings.TrimSpace(account.GetCredential("base_url"))
	if baseURL == "" {
		return nil
	}
	return s.GetCustomEndpointModels(ctx, account.Platform, baseURL)
}
