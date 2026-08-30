package handler

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/logger"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
)

const tmeoaCallbackPath = "/auth/hg-sso/callback"

func (h *Handler) TMEOALogin(w http.ResponseWriter, r *http.Request) {
	if h.cfg.AuthMode != auth.AuthModeTMEOA {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	identity, err := auth.ParseTMEOAIdentity(
		r.Header.Get("X-Token"),
		r.Header.Get("X-Timestamp"),
		r.Header.Get("X-Request-ID"),
		h.cfg.TPPAppSecret,
		time.Now(),
		h.cfg.TMEOAMaxClockSkew,
	)
	if err != nil {
		slog.Warn("tmeoa login rejected", "reason", tmeoaErrorReason(err))
		redirectTMEOAError(w, r)
		return
	}

	user, isNew, err := h.findOrCreateUserWithName(
		r.Context(),
		identity.Email,
		tmeoaDisplayName(identity),
	)
	if err != nil {
		if errors.Is(err, auth.ErrTemporarilyDisabledUser) {
			slog.Warn("tmeoa login rejected", append(logger.RequestAttrs(r), "reason", "user_disabled")...)
		} else {
			slog.Error("tmeoa user lookup failed", append(logger.RequestAttrs(r), "error", err)...)
		}
		redirectTMEOAError(w, r)
		return
	}

	if isNew {
		evt := analytics.Signup(uuidToString(user.ID), user.Email, signupSourceFromRequest(r))
		evt.Properties["auth_method"] = auth.AuthModeTMEOA
		obsmetrics.RecordEvent(h.Analytics, h.Metrics, evt)
	}

	token, err := h.issueJWT(user)
	if err != nil {
		slog.Error("tmeoa session creation failed", append(logger.RequestAttrs(r), "error", err)...)
		redirectTMEOAError(w, r)
		return
	}
	if err := auth.SetAuthCookies(w, token); err != nil {
		slog.Error("tmeoa auth cookie creation failed", append(logger.RequestAttrs(r), "error", err)...)
		redirectTMEOAError(w, r)
		return
	}
	if h.CFSigner != nil {
		for _, cookie := range h.CFSigner.SignedCookies(time.Now().Add(auth.AuthTokenTTL())) {
			http.SetCookie(w, cookie)
		}
	}

	slog.Info("user logged in via tmeoa", append(logger.RequestAttrs(r), "user_id", uuidToString(user.ID))...)
	redirectTMEOASuccess(w, r)
}

func tmeoaDisplayName(identity auth.TMEOAIdentity) string {
	if identity.CName != "" {
		return identity.CName
	}
	return identity.EName
}

func tmeoaErrorReason(err error) string {
	switch {
	case errors.Is(err, auth.ErrTMEOAConfiguration):
		return "configuration"
	case errors.Is(err, auth.ErrTMEOAHeaders):
		return "headers"
	case errors.Is(err, auth.ErrTMEOATimestamp):
		return "timestamp"
	case errors.Is(err, auth.ErrTMEOAToken):
		return "token"
	case errors.Is(err, auth.ErrTMEOAIdentity):
		return "identity"
	default:
		return "unknown"
	}
}

func redirectTMEOASuccess(w http.ResponseWriter, r *http.Request) {
	location := tmeoaCallbackPath
	if query := r.URL.RawQuery; query != "" {
		location += "?" + query
	}
	http.Redirect(w, r, location, http.StatusSeeOther)
}

func redirectTMEOAError(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	query.Set("error", "authentication_failed")
	location := tmeoaCallbackPath + "?" + query.Encode()
	if strings.EqualFold(r.Header.Get("Accept"), "application/json") {
		writeError(w, http.StatusUnauthorized, "enterprise authentication failed")
		return
	}
	http.Redirect(w, r, location, http.StatusSeeOther)
}
