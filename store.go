package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var errNotFound = errors.New("not found")

type Store struct {
	db *sql.DB
}

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	for _, pragma := range []string{
		`PRAGMA journal_mode=WAL;`,
		`PRAGMA synchronous=NORMAL;`,
		`PRAGMA foreign_keys=ON;`,
	} {
		if _, err := db.Exec(pragma); err != nil {
			return nil, err
		}
	}
	var tableExists int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='profiles'`).Scan(&tableExists)
	if tableExists > 0 {
		var hasCountryName int
		_ = db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('profiles') WHERE name = 'country_name'`).Scan(&hasCountryName)
		if hasCountryName == 0 {
			if _, err := db.Exec(`DROP TABLE profiles`); err != nil {
				return nil, err
			}
		}
	}
	schema := `
	CREATE TABLE IF NOT EXISTS profiles (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL UNIQUE,
		gender TEXT NOT NULL,
		gender_probability REAL NOT NULL,
		age INTEGER NOT NULL,
		age_group TEXT NOT NULL,
		country_id TEXT NOT NULL,
		country_name TEXT NOT NULL,
		country_probability REAL NOT NULL,
		created_at TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_profiles_gender ON profiles(gender);
	CREATE INDEX IF NOT EXISTS idx_profiles_country_id ON profiles(country_id);
	CREATE INDEX IF NOT EXISTS idx_profiles_age_group ON profiles(age_group);
	CREATE INDEX IF NOT EXISTS idx_profiles_age ON profiles(age);
	CREATE INDEX IF NOT EXISTS idx_profiles_gender_prob ON profiles(gender_probability);
	CREATE INDEX IF NOT EXISTS idx_profiles_country_prob ON profiles(country_probability);
	CREATE INDEX IF NOT EXISTS idx_profiles_created_at ON profiles(created_at);

	CREATE TABLE IF NOT EXISTS users (
		id TEXT PRIMARY KEY,
		github_id TEXT NOT NULL UNIQUE,
		username TEXT NOT NULL,
		email TEXT,
		avatar_url TEXT,
		role TEXT NOT NULL DEFAULT 'analyst',
		is_active INTEGER NOT NULL DEFAULT 1,
		last_login_at TEXT,
		created_at TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_users_username ON users(username);

	CREATE TABLE IF NOT EXISTS refresh_tokens (
		token_hash TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		created_at TEXT NOT NULL,
		expires_at TEXT NOT NULL,
		revoked_at TEXT,
		FOREIGN KEY (user_id) REFERENCES users(id)
	);
	CREATE INDEX IF NOT EXISTS idx_refresh_user ON refresh_tokens(user_id);
	`
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

const profileColumns = `id, name, gender, gender_probability, age, age_group,
	country_id, country_name, country_probability, created_at`

func scanProfile(row interface {
	Scan(dest ...interface{}) error
}) (*Profile, error) {
	var p Profile
	var createdAt string
	err := row.Scan(
		&p.ID, &p.Name, &p.Gender, &p.GenderProbability, &p.Age, &p.AgeGroup,
		&p.CountryID, &p.CountryName, &p.CountryProbability, &createdAt,
	)
	if err != nil {
		return nil, err
	}
	t, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		t, _ = time.Parse(time.RFC3339, createdAt)
	}
	p.CreatedAt = UTCTime(t.UTC())
	return &p, nil
}

func (s *Store) GetByName(name string) (*Profile, error) {
	row := s.db.QueryRow(`SELECT `+profileColumns+` FROM profiles WHERE LOWER(name) = LOWER(?)`, name)
	p, err := scanProfile(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	return p, err
}

func (s *Store) GetByID(id string) (*Profile, error) {
	row := s.db.QueryRow(`SELECT `+profileColumns+` FROM profiles WHERE id = ?`, id)
	p, err := scanProfile(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	return p, err
}

func (s *Store) InsertIgnore(p *Profile) (bool, error) {
	res, err := s.db.Exec(
		`INSERT OR IGNORE INTO profiles (`+profileColumns+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Gender, p.GenderProbability, p.Age, p.AgeGroup,
		p.CountryID, p.CountryName, p.CountryProbability,
		p.CreatedAt.Time().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Store) Insert(p *Profile) error {
	_, err := s.db.Exec(
		`INSERT INTO profiles (`+profileColumns+`)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Gender, p.GenderProbability, p.Age, p.AgeGroup,
		p.CountryID, p.CountryName, p.CountryProbability,
		p.CreatedAt.Time().UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (s *Store) Delete(id string) error {
	res, err := s.db.Exec(`DELETE FROM profiles WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errNotFound
	}
	return nil
}

func (s *Store) Count() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM profiles`).Scan(&n)
	return n, err
}

type ListFilter struct {
	Gender         string
	CountryID      string
	AgeGroup       string
	MinAge         *int
	MaxAge         *int
	MinGenderProb  *float64
	MinCountryProb *float64

	SortBy string
	Order  string

	Page  int
	Limit int
}

var sortColumns = map[string]string{
	"age":                "age",
	"created_at":         "created_at",
	"gender_probability": "gender_probability",
}

func buildWhere(f ListFilter) (string, []interface{}) {
	var clauses []string
	var args []interface{}
	if f.Gender != "" {
		clauses = append(clauses, `LOWER(gender) = ?`)
		args = append(args, strings.ToLower(f.Gender))
	}
	if f.CountryID != "" {
		clauses = append(clauses, `UPPER(country_id) = ?`)
		args = append(args, strings.ToUpper(f.CountryID))
	}
	if f.AgeGroup != "" {
		clauses = append(clauses, `LOWER(age_group) = ?`)
		args = append(args, strings.ToLower(f.AgeGroup))
	}
	if f.MinAge != nil {
		clauses = append(clauses, `age >= ?`)
		args = append(args, *f.MinAge)
	}
	if f.MaxAge != nil {
		clauses = append(clauses, `age <= ?`)
		args = append(args, *f.MaxAge)
	}
	if f.MinGenderProb != nil {
		clauses = append(clauses, `gender_probability >= ?`)
		args = append(args, *f.MinGenderProb)
	}
	if f.MinCountryProb != nil {
		clauses = append(clauses, `country_probability >= ?`)
		args = append(args, *f.MinCountryProb)
	}
	return strings.Join(clauses, " AND "), args
}

func (s *Store) List(f ListFilter) ([]Profile, int, error) {
	where, args := buildWhere(f)

	countQ := `SELECT COUNT(*) FROM profiles`
	if where != "" {
		countQ += ` WHERE ` + where
	}
	var total int
	if err := s.db.QueryRow(countQ, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	col, ok := sortColumns[f.SortBy]
	if !ok {
		col = "created_at"
	}
	order := "ASC"
	if strings.EqualFold(f.Order, "desc") {
		order = "DESC"
	}

	page := f.Page
	if page < 1 {
		page = 1
	}
	limit := f.Limit
	if limit < 1 {
		limit = 10
	}
	offset := (page - 1) * limit

	q := `SELECT ` + profileColumns + ` FROM profiles`
	if where != "" {
		q += ` WHERE ` + where
	}
	q += fmt.Sprintf(` ORDER BY %s %s, id ASC LIMIT ? OFFSET ?`, col, order)
	args = append(args, limit, offset)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make([]Profile, 0, limit)
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *p)
	}
	return out, total, rows.Err()
}

func (s *Store) ListAll(f ListFilter) ([]Profile, int, error) {
	where, args := buildWhere(f)

	countQ := `SELECT COUNT(*) FROM profiles`
	if where != "" {
		countQ += ` WHERE ` + where
	}
	var total int
	if err := s.db.QueryRow(countQ, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	col, ok := sortColumns[f.SortBy]
	if !ok {
		col = "created_at"
	}
	order := "ASC"
	if strings.EqualFold(f.Order, "desc") {
		order = "DESC"
	}

	q := `SELECT ` + profileColumns + ` FROM profiles`
	if where != "" {
		q += ` WHERE ` + where
	}
	q += fmt.Sprintf(` ORDER BY %s %s, id ASC`, col, order)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make([]Profile, 0, total)
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *p)
	}
	return out, total, rows.Err()
}
