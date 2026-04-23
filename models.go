package main

import "time"

// UTCTime marshals to RFC 3339 UTC ("Z") with second precision, matching the
// Stage 2 response examples (e.g. "2026-04-01T12:00:00Z").
type UTCTime time.Time

func (t UTCTime) MarshalJSON() ([]byte, error) {
	return []byte(`"` + time.Time(t).UTC().Format("2006-01-02T15:04:05Z") + `"`), nil
}

func (t UTCTime) Time() time.Time { return time.Time(t) }

// Profile is the shape returned by all endpoints. Field order mirrors the spec.
type Profile struct {
	ID                 string  `json:"id"`
	Name               string  `json:"name"`
	Gender             string  `json:"gender"`
	GenderProbability  float64 `json:"gender_probability"`
	Age                int     `json:"age"`
	AgeGroup           string  `json:"age_group"`
	CountryID          string  `json:"country_id"`
	CountryName        string  `json:"country_name"`
	CountryProbability float64 `json:"country_probability"`
	CreatedAt          UTCTime `json:"created_at"`
}

type errorResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

type createResponse struct {
	Status  string   `json:"status"`
	Message string   `json:"message,omitempty"`
	Data    *Profile `json:"data"`
}

type getResponse struct {
	Status string   `json:"status"`
	Data   *Profile `json:"data"`
}

// listResponse is the payload for GET /api/profiles and /api/profiles/search.
type listResponse struct {
	Status string    `json:"status"`
	Page   int       `json:"page"`
	Limit  int       `json:"limit"`
	Total  int       `json:"total"`
	Data   []Profile `json:"data"`
}
