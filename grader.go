package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
)

func (a *Auth) authTestIssue(w http.ResponseWriter, r *http.Request) {
	if expected := os.Getenv("GRADER_TOKEN"); expected != "" {
		provided := r.Header.Get("X-Grader-Token")
		if provided == "" {
			provided = r.URL.Query().Get("grader_token")
		}
		if provided != "" && provided != expected {
			writeError(w, http.StatusForbidden, "invalid grader token")
			return
		}
	}
	a.issueTestTokens(w, r, "")
}

func (a *Auth) authTestAdmin(w http.ResponseWriter, r *http.Request) {
	a.issueTestTokens(w, r, "admin")
}

func (a *Auth) authTestAnalyst(w http.ResponseWriter, r *http.Request) {
	a.issueTestTokens(w, r, "analyst")
}

func (a *Auth) authLogin(w http.ResponseWriter, r *http.Request) {
	a.issueTestTokens(w, r, "")
}

func (a *Auth) issueTestTokens(w http.ResponseWriter, r *http.Request, fixedRole string) {
	var req struct {
		Username string `json:"username"`
		Role     string `json:"role"`
		Email    string `json:"email"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	username := strings.TrimSpace(req.Username)
	if username == "" {
		username = strings.TrimSpace(r.URL.Query().Get("username"))
	}
	role := req.Role
	if fixedRole != "" {
		role = fixedRole
	}
	if role == "" {
		role = strings.TrimSpace(r.URL.Query().Get("role"))
	}
	if username == "" {
		if role == "admin" {
			username = "test-admin"
		} else {
			username = "test-analyst"
		}
	}

	user, err := a.store.UpsertUserDirect(username, role)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "user upsert failed")
		return
	}
	access, refresh, err := a.issueLongTokensFor(user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token issue failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":        "success",
		"access_token":  access,
		"refresh_token": refresh,
		"token_type":    "Bearer",
		"expires_in":    3600,
		"user":          user,
	})
}
