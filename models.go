package main

import "time"

// Profile is the shape returned by all endpoints.
type Profile struct {
	ID                 string    `json:"id"`
	Name               string    `json:"name"`
	Gender             string    `json:"gender"`
	GenderProbability  float64   `json:"gender_probability"`
	SampleSize         int       `json:"sample_size"`
	Age                int       `json:"age"`
	AgeGroup           string    `json:"age_group"`
	CountryID          string    `json:"country_id"`
	CountryProbability float64   `json:"country_probability"`
	CreatedAt          time.Time `json:"created_at"`
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
	Status string    `json:"status"`
	Count  int       `json:"count"`
	Data   []Profile `json:"data"`
}
