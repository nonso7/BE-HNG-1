package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
)

type authConfig struct {
	webClientID     string
	webClientSecret string
	webRedirectURI  string

	cliClientID     string
	cliClientSecret string

	webAppURL      string
	adminUsernames map[string]bool
}

type Auth struct {
	cfg    authConfig
	store  *Store
	signer *tokenSigner
	pkce   *pkceStore
}

func newAuth(cfg authConfig, store *Store, signer *tokenSigner) *Auth {
	return &Auth{cfg: cfg, store: store, signer: signer, pkce: newPKCEStore()}
}

func pkceChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func (a *Auth) authGithub(w http.ResponseWriter, r *http.Request) {
	if a.cfg.webClientID == "" {
		writeError(w, http.StatusServiceUnavailable, "GitHub OAuth not configured")
		return
	}
	state := randomURLSafe(32)
	verifier := randomURLSafe(64)
	a.pkce.Set(state, verifier)
	challenge := pkceChallenge(verifier)
	q := url.Values{}
	q.Set("client_id", a.cfg.webClientID)
	q.Set("redirect_uri", a.cfg.webRedirectURI)
	q.Set("scope", "read:user user:email")
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	target := "https://github.com/login/oauth/authorize?" + q.Encode()
	http.Redirect(w, r, target, http.StatusFound)
}

func (a *Auth) authGithubCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || state == "" {
		writeError(w, http.StatusBadRequest, "Missing code or state")
		return
	}
	verifier, ok := a.pkce.PopVerifier(state)
	if !ok {
		writeError(w, http.StatusBadRequest, "Invalid or expired state")
		return
	}
	user, access, refresh, err := a.exchangeAndIssue(code, verifier, a.cfg.webRedirectURI, a.cfg.webClientID, a.cfg.webClientSecret)
	if err != nil {
		log.Printf("auth callback: %v", err)
		writeError(w, http.StatusBadGateway, "GitHub OAuth exchange failed")
		return
	}
	setAuthCookies(w, r, access, refresh)
	if a.cfg.webAppURL != "" {
		w.Header().Set("Refresh", "0; url="+a.cfg.webAppURL)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":        "success",
		"access_token":  access,
		"refresh_token": refresh,
		"token_type":    "Bearer",
		"expires_in":    int(accessTokenTTL.Seconds()),
		"user":          user,
	})
}

func (a *Auth) authGithubExchange(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code         string `json:"code"`
		CodeVerifier string `json:"code_verifier"`
		RedirectURI  string `json:"redirect_uri"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Code == "" || req.CodeVerifier == "" {
		writeError(w, http.StatusBadRequest, "code and code_verifier required")
		return
	}
	redirect := req.RedirectURI
	if redirect == "" {
		redirect = "http://127.0.0.1:9876/callback"
	}
	user, access, refresh, err := a.exchangeAndIssue(req.Code, req.CodeVerifier, redirect, a.cfg.cliClientID, a.cfg.cliClientSecret)
	if err != nil {
		log.Printf("cli exchange: %v", err)
		writeError(w, http.StatusBadGateway, "GitHub OAuth exchange failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":        "success",
		"access_token":  access,
		"refresh_token": refresh,
		"user":          user,
	})
}

func (a *Auth) exchangeAndIssue(code, verifier, redirectURI, clientID, clientSecret string) (*User, string, string, error) {
	if clientID == "" || clientSecret == "" {
		return nil, "", "", errors.New("github oauth not configured for this flow")
	}
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("code_verifier", verifier)
	req, err := http.NewRequest("POST", "https://github.com/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, "", "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, "", "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var tok struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, "", "", fmt.Errorf("decode token: %w", err)
	}
	if tok.AccessToken == "" {
		return nil, "", "", fmt.Errorf("github exchange: %s %s", tok.Error, tok.ErrorDesc)
	}
	uReq, _ := http.NewRequest("GET", "https://api.github.com/user", nil)
	uReq.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	uReq.Header.Set("Accept", "application/vnd.github+json")
	uResp, err := httpClient.Do(uReq)
	if err != nil {
		return nil, "", "", err
	}
	defer uResp.Body.Close()
	ub, _ := io.ReadAll(uResp.Body)
	var gu struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Email     string `json:"email"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := json.Unmarshal(ub, &gu); err != nil {
		return nil, "", "", fmt.Errorf("decode user: %w", err)
	}
	if gu.Login == "" {
		return nil, "", "", errors.New("github user missing login")
	}
	user, err := a.store.UpsertUserFromGithub(fmt.Sprint(gu.ID), gu.Login, gu.Email, gu.AvatarURL, a.cfg.adminUsernames)
	if err != nil {
		return nil, "", "", err
	}
	access, refresh, err := a.issueTokensFor(user)
	if err != nil {
		return nil, "", "", err
	}
	return user, access, refresh, nil
}

func (a *Auth) issueTokensFor(user *User) (string, string, error) {
	access, err := a.signer.issueAccess(user.ID, user.Username, user.Role)
	if err != nil {
		return "", "", err
	}
	refresh, err := generateRefreshToken()
	if err != nil {
		return "", "", err
	}
	if err := a.store.StoreRefreshToken(refresh, user.ID, refreshTokenTTL); err != nil {
		return "", "", err
	}
	return access, refresh, nil
}

func (a *Auth) authRefresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.RefreshToken == "" {
		if c, err := r.Cookie("refresh_token"); err == nil {
			req.RefreshToken = c.Value
		}
	}
	if req.RefreshToken == "" {
		writeError(w, http.StatusBadRequest, "refresh_token required")
		return
	}
	userID, err := a.store.ValidateAndRevokeRefreshToken(req.RefreshToken)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "Invalid or expired refresh token")
		return
	}
	user, err := a.store.GetUserByID(userID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "User not found")
		return
	}
	if !user.IsActive {
		writeError(w, http.StatusForbidden, "Account disabled")
		return
	}
	access, refresh, err := a.issueTokensFor(user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to issue tokens")
		return
	}
	setAuthCookies(w, r, access, refresh)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":        "success",
		"access_token":  access,
		"refresh_token": refresh,
	})
}

func (a *Auth) authLogout(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.RefreshToken == "" {
		if c, err := r.Cookie("refresh_token"); err == nil {
			req.RefreshToken = c.Value
		}
	}
	if req.RefreshToken != "" {
		_ = a.store.RevokeRefreshToken(req.RefreshToken)
	}
	clearAuthCookies(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "success", "message": "Logged out"})
}

func (a *Auth) authCLIConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":    "success",
		"client_id": a.cfg.cliClientID,
	})
}

func (a *Auth) authCSRF(w http.ResponseWriter, r *http.Request) {
	tok := randomURLSafe(24)
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, &http.Cookie{
		Name:     "csrf_token",
		Value:    tok,
		Path:     "/",
		HttpOnly: false,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(refreshTokenTTL.Seconds()),
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "success", "csrf_token": tok})
}

func setAuthCookies(w http.ResponseWriter, r *http.Request, access, refresh string) {
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, &http.Cookie{
		Name:     "access_token",
		Value:    access,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(accessTokenTTL.Seconds()),
	})
	http.SetCookie(w, &http.Cookie{
		Name:     "refresh_token",
		Value:    refresh,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(refreshTokenTTL.Seconds()),
	})
	csrfToken := randomURLSafe(24)
	http.SetCookie(w, &http.Cookie{
		Name:     "csrf_token",
		Value:    csrfToken,
		Path:     "/",
		HttpOnly: false,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(refreshTokenTTL.Seconds()),
	})
}

func clearAuthCookies(w http.ResponseWriter) {
	for _, name := range []string{"access_token", "refresh_token", "csrf_token"} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: name != "csrf_token",
		})
	}
}
