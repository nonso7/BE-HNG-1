package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	accessTokenTTL  = 3 * time.Minute
	refreshTokenTTL = 5 * time.Minute
)

var errInvalidToken = errors.New("invalid token")

type AccessClaims struct {
	Sub      string `json:"sub"`
	Username string `json:"username"`
	Role     string `json:"role"`
	Iat      int64  `json:"iat"`
	Exp      int64  `json:"exp"`
}

type tokenSigner struct {
	secret []byte
}

func newTokenSigner(secret string) *tokenSigner {
	return &tokenSigner{secret: []byte(secret)}
}

func (t *tokenSigner) issueAccess(sub, username, role string) (string, error) {
	return t.issueAccessWithTTL(sub, username, role, accessTokenTTL)
}

func (t *tokenSigner) issueAccessWithTTL(sub, username, role string, ttl time.Duration) (string, error) {
	now := time.Now().Unix()
	claims := AccessClaims{
		Sub:      sub,
		Username: username,
		Role:     role,
		Iat:      now,
		Exp:      now + int64(ttl.Seconds()),
	}
	header := `{"alg":"HS256","typ":"JWT"}`
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	h := base64.RawURLEncoding.EncodeToString([]byte(header))
	p := base64.RawURLEncoding.EncodeToString(payload)
	signing := h + "." + p
	mac := hmac.New(sha256.New, t.secret)
	mac.Write([]byte(signing))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return signing + "." + sig, nil
}

func (t *tokenSigner) parseAccess(tok string) (*AccessClaims, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return nil, errInvalidToken
	}
	signing := parts[0] + "." + parts[1]
	sigGiven, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errInvalidToken
	}
	mac := hmac.New(sha256.New, t.secret)
	mac.Write([]byte(signing))
	if !hmac.Equal(sigGiven, mac.Sum(nil)) {
		return nil, errInvalidToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errInvalidToken
	}
	var claims AccessClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, errInvalidToken
	}
	if time.Now().Unix() >= claims.Exp {
		return nil, errInvalidToken
	}
	return &claims, nil
}

func generateRefreshToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func hashRefreshToken(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

func randomURLSafe(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
