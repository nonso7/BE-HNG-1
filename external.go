package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

var httpClient = &http.Client{Timeout: 20 * time.Second}

type genderizeResp struct {
	Name        string   `json:"name"`
	Gender      *string  `json:"gender"`
	Probability float64  `json:"probability"`
	Count       int      `json:"count"`
}

type agifyResp struct {
	Name  string `json:"name"`
	Age   *int   `json:"age"`
	Count int    `json:"count"`
}

type nationalizeCountry struct {
	CountryID   string  `json:"country_id"`
	Probability float64 `json:"probability"`
}

type nationalizeResp struct {
	Name    string               `json:"name"`
	Count   int                  `json:"count"`
	Country []nationalizeCountry `json:"country"`
}

// upstreamError carries the name of the API that failed, to build the
// mandated "${externalApi} returned an invalid response" message.
type upstreamError struct {
	API string
}

func (e *upstreamError) Error() string {
	return fmt.Sprintf("%s returned an invalid response", e.API)
}

func fetchJSON(u string, out interface{}) error {
	resp, err := httpClient.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("upstream status %d: %s", resp.StatusCode, string(body))
	}
	return json.Unmarshal(body, out)
}

func fetchGenderize(name string) (gender string, probability float64, sample int, err error) {
	var r genderizeResp
	if err := fetchJSON("https://api.genderize.io?name="+url.QueryEscape(name), &r); err != nil {
		return "", 0, 0, &upstreamError{API: "Genderize"}
	}
	if r.Gender == nil || *r.Gender == "" || r.Count == 0 {
		return "", 0, 0, &upstreamError{API: "Genderize"}
	}
	return *r.Gender, r.Probability, r.Count, nil
}

func fetchAgify(name string) (int, error) {
	var r agifyResp
	if err := fetchJSON("https://api.agify.io?name="+url.QueryEscape(name), &r); err != nil {
		return 0, &upstreamError{API: "Agify"}
	}
	if r.Age == nil {
		return 0, &upstreamError{API: "Agify"}
	}
	return *r.Age, nil
}

func fetchNationalize(name string) (countryID string, probability float64, err error) {
	var r nationalizeResp
	if err := fetchJSON("https://api.nationalize.io?name="+url.QueryEscape(name), &r); err != nil {
		return "", 0, &upstreamError{API: "Nationalize"}
	}
	if len(r.Country) == 0 {
		return "", 0, &upstreamError{API: "Nationalize"}
	}
	best := r.Country[0]
	for _, c := range r.Country[1:] {
		if c.Probability > best.Probability {
			best = c
		}
	}
	if best.CountryID == "" {
		return "", 0, &upstreamError{API: "Nationalize"}
	}
	return best.CountryID, best.Probability, nil
}
