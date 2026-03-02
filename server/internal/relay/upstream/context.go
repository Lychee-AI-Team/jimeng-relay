package upstream

import (
	"context"
)

type contextKey string

const apiKeyIDKey contextKey = "upstream_api_key_id"

// WithAPIKeyID returns a new context with the given API key ID.
func WithAPIKeyID(ctx context.Context, apiKeyID string) context.Context {
	return context.WithValue(ctx, apiKeyIDKey, apiKeyID)
}

// GetAPIKeyID returns the API key ID from the context, or an empty string if not present.
func GetAPIKeyID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v := ctx.Value(apiKeyIDKey); v != nil {
		if apiKeyID, ok := v.(string); ok {
			return apiKeyID
		}
	}
	return ""
}
