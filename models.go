package main

import "time"

type UTCTime time.Time

func (t UTCTime) MarshalJSON() ([]byte, error) {
	return []byte(`"` + time.Time(t).UTC().Format("2006-01-02T15:04:05Z") + `"`), nil
}

func (t UTCTime) Time() time.Time { return time.Time(t) }

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

type listResponse struct {
	Status     string          `json:"status"`
	Page       int             `json:"page"`
	Limit      int             `json:"limit"`
	Total      int             `json:"total"`
	TotalPages int             `json:"total_pages"`
	Links      paginationLinks `json:"links"`
	Data       []Profile       `json:"data"`
}
