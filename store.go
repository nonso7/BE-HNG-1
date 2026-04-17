package main

import (
	"database/sql"
	"errors"
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
	if _, err := db.Exec(`PRAGMA journal_mode=WAL;`); err != nil {
		return nil, err
	}
	schema := `
	CREATE TABLE IF NOT EXISTS profiles (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		name_key TEXT NOT NULL UNIQUE,
		gender TEXT NOT NULL,
		gender_probability REAL NOT NULL,
		sample_size INTEGER NOT NULL,
		age INTEGER NOT NULL,
		age_group TEXT NOT NULL,
		country_id TEXT NOT NULL,
		country_probability REAL NOT NULL,
		created_at TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_profiles_gender ON profiles(gender);
	CREATE INDEX IF NOT EXISTS idx_profiles_country ON profiles(country_id);
	CREATE INDEX IF NOT EXISTS idx_profiles_age_group ON profiles(age_group);
	`
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func nameKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func scanProfile(row interface {
	Scan(dest ...interface{}) error
}) (*Profile, error) {
	var p Profile
	var createdAt string
	err := row.Scan(
		&p.ID, &p.Name, &p.Gender, &p.GenderProbability, &p.SampleSize,
		&p.Age, &p.AgeGroup, &p.CountryID, &p.CountryProbability, &createdAt,
	)
	if err != nil {
		return nil, err
	}
	t, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		// fallback
		t, _ = time.Parse(time.RFC3339, createdAt)
	}
	p.CreatedAt = t.UTC()
	return &p, nil
}

const profileColumns = `id, name, gender, gender_probability, sample_size,
	age, age_group, country_id, country_probability, created_at`

func (s *Store) GetByName(name string) (*Profile, error) {
	row := s.db.QueryRow(`SELECT `+profileColumns+` FROM profiles WHERE name_key = ?`, nameKey(name))
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

func (s *Store) Insert(p *Profile) error {
	_, err := s.db.Exec(
		`INSERT INTO profiles (id, name, name_key, gender, gender_probability, sample_size,
			age, age_group, country_id, country_probability, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, nameKey(p.Name), p.Gender, p.GenderProbability, p.SampleSize,
		p.Age, p.AgeGroup, p.CountryID, p.CountryProbability, p.CreatedAt.UTC().Format(time.RFC3339Nano),
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

type ListFilter struct {
	Gender    string
	CountryID string
	AgeGroup  string
}

func (s *Store) List(f ListFilter) ([]Profile, error) {
	q := `SELECT ` + profileColumns + ` FROM profiles WHERE 1=1`
	args := []interface{}{}
	if f.Gender != "" {
		q += ` AND LOWER(gender) = ?`
		args = append(args, strings.ToLower(f.Gender))
	}
	if f.CountryID != "" {
		q += ` AND LOWER(country_id) = ?`
		args = append(args, strings.ToLower(f.CountryID))
	}
	if f.AgeGroup != "" {
		q += ` AND LOWER(age_group) = ?`
		args = append(args, strings.ToLower(f.AgeGroup))
	}
	q += ` ORDER BY created_at DESC`

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Profile, 0)
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}
