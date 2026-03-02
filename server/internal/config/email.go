package config

import (
	"os"
	"strings"
)

const (
	EnvAdminResetBaseURL = "ADMIN_RESET_BASE_URL"
	EnvEmailFrom         = "EMAIL_FROM"
	EnvEmailProvider     = "EMAIL_PROVIDER"
)

const (
	DefaultAdminResetBaseURL = "http://localhost:8080/admin/reset"
	DefaultEmailFrom         = "no-reply@jimeng-relay.local"
	DefaultEmailProvider     = "noop"
)

type EmailConfig struct {
	AdminResetBaseURL string
	From              string
	Provider          string
}

func LoadEmailConfigFromEnv() EmailConfig {
	baseURL := strings.TrimSpace(os.Getenv(EnvAdminResetBaseURL))
	if baseURL == "" {
		baseURL = DefaultAdminResetBaseURL
	}

	from := strings.TrimSpace(os.Getenv(EnvEmailFrom))
	if from == "" {
		from = DefaultEmailFrom
	}

	provider := strings.ToLower(strings.TrimSpace(os.Getenv(EnvEmailProvider)))
	if provider == "" {
		provider = DefaultEmailProvider
	}

	return EmailConfig{
		AdminResetBaseURL: baseURL,
		From:              from,
		Provider:          provider,
	}
}
