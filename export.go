package main

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

func (s *Server) exportProfiles(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "csv"
	}
	if format != "csv" {
		writeError(w, http.StatusBadRequest, "Only csv format supported")
		return
	}
	filter, err := parseListQuery(r)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	profiles, _, err := s.store.ListAll(filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	ts := time.Now().UTC().Format("20060102T150405Z")
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="profiles_%s.csv"`, ts))
	cw := csv.NewWriter(w)
	defer cw.Flush()
	_ = cw.Write([]string{"id", "name", "gender", "gender_probability", "age", "age_group", "country_id", "country_name", "country_probability", "created_at"})
	for _, p := range profiles {
		_ = cw.Write([]string{
			p.ID, p.Name, p.Gender,
			strconv.FormatFloat(p.GenderProbability, 'f', -1, 64),
			strconv.Itoa(p.Age),
			p.AgeGroup, p.CountryID, p.CountryName,
			strconv.FormatFloat(p.CountryProbability, 'f', -1, 64),
			p.CreatedAt.Time().UTC().Format(time.RFC3339),
		})
	}
}
