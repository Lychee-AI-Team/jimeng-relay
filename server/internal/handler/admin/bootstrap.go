package admin

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	internalerrors "github.com/jimeng-relay/server/internal/errors"
	adminservice "github.com/jimeng-relay/server/internal/service/admin"
)

const defaultSessionCookieName = "admin_session"

type BootstrapHandler struct {
	service           *adminservice.Service
	sessionCookieName string
}

type bootstrapRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func NewBootstrapHandler(service *adminservice.Service, logger *slog.Logger) *BootstrapHandler {
	if logger == nil {
		logger = slog.Default()
	}
	_ = logger
	return &BootstrapHandler{service: service, sessionCookieName: defaultSessionCookieName}
}

func (h *BootstrapHandler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/bootstrap", h.handleBootstrap)
	return mux
}

func (h *BootstrapHandler) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, internalerrors.New(internalerrors.ErrValidationFailed, "method not allowed", nil), http.StatusMethodNotAllowed)
		return
	}
	if h.service == nil {
		writeError(w, internalerrors.New(internalerrors.ErrInternalError, "admin bootstrap service is not configured", nil), http.StatusInternalServerError)
		return
	}

	var req bootstrapRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, internalerrors.New(internalerrors.ErrValidationFailed, "invalid bootstrap payload", err), http.StatusBadRequest)
		return
	}

	result, err := h.service.Bootstrap(r.Context(), adminservice.BootstrapRequest{
		Email:    strings.TrimSpace(req.Email),
		Password: req.Password,
	})
	if err != nil {
		if adminservice.IsBootstrapLocked(err) {
			writeError(w, internalerrors.New(internalerrors.ErrValidationFailed, "admin bootstrap is locked", err), http.StatusConflict)
			return
		}
		writeError(w, err, statusFromError(err))
		return
	}

	expiresIn := int(time.Until(result.SessionExpiresAt).Seconds())
	if expiresIn < 0 {
		expiresIn = 0
	}
	http.SetCookie(w, &http.Cookie{
		Name:     h.sessionCookieName,
		Value:    result.SessionToken,
		Path:     "/admin",
		Expires:  result.SessionExpiresAt,
		MaxAge:   expiresIn,
		HttpOnly: true,
		Secure:   !isLocalhostRequest(r),
		SameSite: http.SameSiteStrictMode,
	})

	writeJSON(w, http.StatusCreated, map[string]any{
		"admin_user": map[string]any{
			"id":         result.AdminUser.ID,
			"email":      result.AdminUser.Email,
			"created_at": result.AdminUser.CreatedAt,
			"updated_at": result.AdminUser.UpdatedAt,
		},
		"session_expires_at": result.SessionExpiresAt,
	})
}

func statusFromError(err error) int {
	switch internalerrors.GetCode(err) {
	case internalerrors.ErrValidationFailed:
		return http.StatusBadRequest
	case internalerrors.ErrDatabaseError, internalerrors.ErrInternalError:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, err error, status int) {
	if status <= 0 {
		status = statusFromError(err)
	}
	code := internalerrors.GetCode(err)
	if code == "" {
		code = internalerrors.ErrUnknown
	}
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": err.Error(),
		},
	})
}
