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
	// Drop a pre-Stage-2 profiles table so the new schema below can apply
	// cleanly on top of an old persistent volume.
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

// InsertIgnore inserts a profile. If a profile with the same name already
// exists, it silently does nothing. Returns (true, nil) on actual insert.
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

// Insert inserts a profile, erroring on duplicate name.
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

// ListFilter carries every dimension of the Stage 2 list query:
// filters, sort, and pagination.
type ListFilter struct {
	Gender         string
	CountryID      string
	AgeGroup       string
	MinAge         *int
	MaxAge         *int
	MinGenderProb  *float64
	MinCountryProb *float64

	SortBy string // "age" | "created_at" | "gender_probability"
	Order  string // "asc" | "desc"

	Page  int // 1-indexed
	Limit int
}

// sortColumns whitelists which columns are sortable. Keys are the user-facing
// names; values are the real SQL column names. Never interpolate a non-whitelist
// value into SQL.
var sortColumns = map[string]string{
	"age":                "age",
	"created_at":         "created_at",
	"gender_probability": "gender_probability",
}

// buildWhere returns the WHERE fragment (without the "WHERE" keyword) and its
// bind args. Callers prepend "WHERE " when there's at least one clause.
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

// List runs the filter + sort + paginate query and also returns the total
// matching count (for pagination metadata).
func (s *Store) List(f ListFilter) ([]Profile, int, error) {
	where, args := buildWhere(f)

	// Total count (same WHERE, no LIMIT/OFFSET).
	countQ := `SELECT COUNT(*) FROM profiles`
	if where != "" {
		countQ += ` WHERE ` + where
	}
	var total int
	if err := s.db.QueryRow(countQ, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	// Validate + resolve sort column.
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
	// Tie-break on id so ordering is stable for pagination when the sort key
	// has ties (common with age / gender_probability).
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
