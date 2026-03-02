package admin

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jimeng-relay/server/internal/config"
)

type EmailProvider interface {
	SendPasswordReset(ctx context.Context, toEmail, resetLink string) error
}

type NoOpEmailProvider struct {
	logger *slog.Logger
	from   string
}

func NewEmailProvider(cfg config.EmailConfig, logger *slog.Logger) EmailProvider {
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if provider == "" || provider == "noop" {
		return NewNoOpEmailProvider(cfg.From, logger)
	}

	if logger == nil {
		logger = slog.Default()
	}
	logger.Warn("unknown email provider, fallback to noop", "provider", provider)
	return NewNoOpEmailProvider(cfg.From, logger)
}

func NewNoOpEmailProvider(from string, logger *slog.Logger) *NoOpEmailProvider {
	if logger == nil {
		logger = slog.Default()
	}
	from = strings.TrimSpace(from)
	if from == "" {
		from = config.DefaultEmailFrom
	}
	return &NoOpEmailProvider{logger: logger, from: from}
}

func (p *NoOpEmailProvider) SendPasswordReset(ctx context.Context, toEmail, resetLink string) error {
	if p == nil {
		return fmt.Errorf("email provider is nil")
	}
	toEmail = strings.TrimSpace(toEmail)
	resetLink = strings.TrimSpace(resetLink)
	if toEmail == "" {
		return fmt.Errorf("to email is required")
	}
	if resetLink == "" {
		return fmt.Errorf("reset link is required")
	}

	p.logger.InfoContext(ctx, "password reset email (noop)",
		"from", p.from,
		"to", toEmail,
		"reset_link", resetLink,
	)
	return nil
}
