package main

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

type User struct {
	ID          string   `json:"id"`
	GithubID    string   `json:"github_id"`
	Username    string   `json:"username"`
	Email       string   `json:"email,omitempty"`
	AvatarURL   string   `json:"avatar_url,omitempty"`
	Role        string   `json:"role"`
	IsActive    bool     `json:"is_active"`
	LastLoginAt *UTCTime `json:"last_login_at,omitempty"`
	CreatedAt   UTCTime  `json:"created_at"`
}

const userColumns = `id, github_id, username, email, avatar_url, role, is_active, last_login_at, created_at`

func scanUser(row interface {
	Scan(dest ...interface{}) error
}) (*User, error) {
	var u User
	var email, avatar, lastLogin sql.NullString
	var createdAt string
	var isActive int
	if err := row.Scan(&u.ID, &u.GithubID, &u.Username, &email, &avatar, &u.Role, &isActive, &lastLogin, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errNotFound
		}
		return nil, err
	}
	u.Email = email.String
	u.AvatarURL = avatar.String
	u.IsActive = isActive == 1
	if t, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
		u.CreatedAt = UTCTime(t.UTC())
	}
	if lastLogin.Valid {
		if t, err := time.Parse(time.RFC3339Nano, lastLogin.String); err == nil {
			ut := UTCTime(t.UTC())
			u.LastLoginAt = &ut
		}
	}
	return &u, nil
}

func (s *Store) GetUserByID(id string) (*User, error) {
	row := s.db.QueryRow(`SELECT `+userColumns+` FROM users WHERE id = ?`, id)
	return scanUser(row)
}

func (s *Store) GetUserByGithubID(ghid string) (*User, error) {
	row := s.db.QueryRow(`SELECT `+userColumns+` FROM users WHERE github_id = ?`, ghid)
	return scanUser(row)
}

func (s *Store) GetUserByUsername(username string) (*User, error) {
	row := s.db.QueryRow(`SELECT `+userColumns+` FROM users WHERE username = ?`, username)
	return scanUser(row)
}

func (s *Store) UpsertUserFromGithub(ghid, username, email, avatar string, adminUsernames map[string]bool) (*User, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	existing, err := s.GetUserByGithubID(ghid)
	if err == nil {
		if _, err := s.db.Exec(`UPDATE users SET username=?, email=?, avatar_url=?, last_login_at=? WHERE id=?`,
			username, email, avatar, now, existing.ID); err != nil {
			return nil, err
		}
		return s.GetUserByID(existing.ID)
	}
	if !errors.Is(err, errNotFound) {
		return nil, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	role := "analyst"
	if adminUsernames[strings.ToLower(username)] {
		role = "admin"
	}
	if _, err := s.db.Exec(
		`INSERT INTO users (id, github_id, username, email, avatar_url, role, is_active, last_login_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)`,
		id.String(), ghid, username, email, avatar, role, now, now,
	); err != nil {
		return nil, err
	}
	return s.GetUserByID(id.String())
}

func (s *Store) UpsertUserDirect(username, role string) (*User, error) {
	if role != "admin" && role != "analyst" {
		role = "analyst"
	}
	existing, err := s.GetUserByUsername(username)
	if err == nil {
		if existing.Role != role {
			if _, err := s.db.Exec(`UPDATE users SET role=? WHERE id=?`, role, existing.ID); err != nil {
				return nil, err
			}
			return s.GetUserByID(existing.ID)
		}
		return existing, nil
	}
	if !errors.Is(err, errNotFound) {
		return nil, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	ghid := "test-" + username
	if _, err := s.db.Exec(
		`INSERT INTO users (id, github_id, username, email, avatar_url, role, is_active, last_login_at, created_at)
		 VALUES (?, ?, ?, '', '', ?, 1, ?, ?)`,
		id.String(), ghid, username, role, now, now,
	); err != nil {
		return nil, err
	}
	return s.GetUserByID(id.String())
}

func (s *Store) StoreRefreshToken(token, userID string, ttl time.Duration) error {
	hash := hashRefreshToken(token)
	now := time.Now().UTC()
	expires := now.Add(ttl)
	_, err := s.db.Exec(
		`INSERT INTO refresh_tokens (token_hash, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		hash, userID, now.Format(time.RFC3339Nano), expires.Format(time.RFC3339Nano),
	)
	return err
}

func (s *Store) ValidateAndRevokeRefreshToken(token string) (string, error) {
	hash := hashRefreshToken(token)
	var userID, expiresAt string
	var revokedAt sql.NullString
	row := s.db.QueryRow(`SELECT user_id, expires_at, revoked_at FROM refresh_tokens WHERE token_hash = ?`, hash)
	if err := row.Scan(&userID, &expiresAt, &revokedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", errInvalidToken
		}
		return "", err
	}
	if revokedAt.Valid {
		return "", errInvalidToken
	}
	exp, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil || time.Now().UTC().After(exp) {
		return "", errInvalidToken
	}
	if _, err := s.db.Exec(`UPDATE refresh_tokens SET revoked_at = ? WHERE token_hash = ?`,
		time.Now().UTC().Format(time.RFC3339Nano), hash); err != nil {
		return "", err
	}
	return userID, nil
}

func (s *Store) RevokeRefreshToken(token string) error {
	hash := hashRefreshToken(token)
	_, err := s.db.Exec(`UPDATE refresh_tokens SET revoked_at = ? WHERE token_hash = ? AND revoked_at IS NULL`,
		time.Now().UTC().Format(time.RFC3339Nano), hash)
	return err
}
