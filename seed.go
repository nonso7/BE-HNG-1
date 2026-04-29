package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
)

//go:embed profiles_seed.json
var seedData []byte

type seedEntry struct {
	Name               string  `json:"name"`
	Gender             string  `json:"gender"`
	GenderProbability  float64 `json:"gender_probability"`
	Age                int     `json:"age"`
	AgeGroup           string  `json:"age_group"`
	CountryID          string  `json:"country_id"`
	CountryName        string  `json:"country_name"`
	CountryProbability float64 `json:"country_probability"`
}

type seedFile struct {
	Profiles []seedEntry `json:"profiles"`
}

func parseSeed() ([]seedEntry, error) {
	var sf seedFile
	if err := json.Unmarshal(seedData, &sf); err != nil {
		return nil, fmt.Errorf("parse seed: %w", err)
	}
	return sf.Profiles, nil
}

func SeedProfiles(s *Store) (int, error) {
	entries, err := parseSeed()
	if err != nil {
		return 0, err
	}

	current, err := s.Count()
	if err == nil && current >= len(entries) {
		log.Printf("seed: %d rows already present, skipping", current)
		return 0, nil
	}

	start := time.Now().UTC().Add(-time.Duration(len(entries)) * time.Millisecond)

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	stmt, err := tx.Prepare(
		`INSERT OR IGNORE INTO profiles (` + profileColumns + `)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
	)
	if err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	defer stmt.Close()

	inserted := 0
	for i, e := range entries {
		id, err := uuid.NewV7()
		if err != nil {
			_ = tx.Rollback()
			return inserted, err
		}
		createdAt := start.Add(time.Duration(i) * time.Millisecond).Format(time.RFC3339Nano)
		res, err := stmt.Exec(
			id.String(), e.Name, e.Gender, e.GenderProbability,
			e.Age, e.AgeGroup, e.CountryID, e.CountryName, e.CountryProbability,
			createdAt,
		)
		if err != nil {
			_ = tx.Rollback()
			return inserted, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
		}
	}
	if err := tx.Commit(); err != nil {
		return inserted, err
	}
	log.Printf("seed: %d profile(s) inserted (skipped %d duplicates)", inserted, len(entries)-inserted)
	return inserted, nil
}

func SeedCountryMap() (map[string]string, error) {
	entries, err := parseSeed()
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, 80)
	for _, e := range entries {
		if e.CountryID != "" && e.CountryName != "" {
			m[e.CountryID] = e.CountryName
		}
	}
	return m, nil
}
