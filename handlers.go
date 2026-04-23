package main

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Server struct {
	store       *Store
	codeToName  map[string]string // ISO-2 → country name (for POST + NL search)
}

func NewServer(store *Store) *Server {
	codeMap, err := SeedCountryMap()
	if err != nil {
		log.Printf("warn: could not build country map from seed: %v", err)
		codeMap = map[string]string{}
	}
	return &Server{store: store, codeToName: codeMap}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/profiles", s.createProfile)
	mux.HandleFunc("GET /api/profiles", s.listProfiles)
	// "search" is a literal segment so Go 1.22 mux ranks it above {id}.
	mux.HandleFunc("GET /api/profiles/search", s.searchProfiles)
	mux.HandleFunc("GET /api/profiles/{id}", s.getProfile)
	mux.HandleFunc("DELETE /api/profiles/{id}", s.deleteProfile)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{
			"status":  "success",
			"message": "Insighta Labs Profile API. Try /api/profiles or /api/profiles/search?q=...",
		})
	})
	return withCORS(mux)
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorResponse{Status: "error", Message: message})
}

// --- POST /api/profiles ---

func (s *Server) createProfile(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if len(body) == 0 {
		writeError(w, http.StatusBadRequest, "Name is required")
		return
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	rawName, present := raw["name"]
	if !present {
		writeError(w, http.StatusBadRequest, "Name is required")
		return
	}
	var name string
	if err := json.Unmarshal(rawName, &name); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "Name must be a string")
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "Name is required")
		return
	}

	if existing, err := s.store.GetByName(name); err == nil {
		writeJSON(w, http.StatusOK, createResponse{
			Status:  "success",
			Message: "Profile already exists",
			Data:    existing,
		})
		return
	} else if !errors.Is(err, errNotFound) {
		log.Printf("store.GetByName: %v", err)
		writeError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	gender, genderProb, _, err := fetchGenderize(name)
	if err != nil {
		writeError(w, http.StatusBadGateway, upstreamMessage(err, "Genderize"))
		return
	}
	age, err := fetchAgify(name)
	if err != nil {
		writeError(w, http.StatusBadGateway, upstreamMessage(err, "Agify"))
		return
	}
	countryID, countryProb, err := fetchNationalize(name)
	if err != nil {
		writeError(w, http.StatusBadGateway, upstreamMessage(err, "Nationalize"))
		return
	}

	id, err := uuid.NewV7()
	if err != nil {
		log.Printf("uuid.NewV7: %v", err)
		writeError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	countryName := s.codeToName[countryID]
	if countryName == "" {
		countryName = countryID
	}

	profile := &Profile{
		ID:                 id.String(),
		Name:               name,
		Gender:             gender,
		GenderProbability:  genderProb,
		Age:                age,
		AgeGroup:           classifyAge(age),
		CountryID:          countryID,
		CountryName:        countryName,
		CountryProbability: countryProb,
		CreatedAt:          UTCTime(time.Now().UTC()),
	}

	if err := s.store.Insert(profile); err != nil {
		if existing, gerr := s.store.GetByName(name); gerr == nil {
			writeJSON(w, http.StatusOK, createResponse{
				Status:  "success",
				Message: "Profile already exists",
				Data:    existing,
			})
			return
		}
		log.Printf("store.Insert: %v", err)
		writeError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	writeJSON(w, http.StatusCreated, createResponse{Status: "success", Data: profile})
}

func upstreamMessage(err error, api string) string {
	if ue, ok := err.(*upstreamError); ok {
		return ue.Error()
	}
	return api + " returned an invalid response"
}

// --- GET /api/profiles/{id} ---

func (s *Server) getProfile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, err := s.store.GetByID(id)
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "Profile not found")
		return
	}
	if err != nil {
		log.Printf("store.GetByID: %v", err)
		writeError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	writeJSON(w, http.StatusOK, getResponse{Status: "success", Data: p})
}

// --- DELETE /api/profiles/{id} ---

func (s *Server) deleteProfile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := s.store.Delete(id)
	if errors.Is(err, errNotFound) {
		writeError(w, http.StatusNotFound, "Profile not found")
		return
	}
	if err != nil {
		log.Printf("store.Delete: %v", err)
		writeError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- GET /api/profiles ---

func (s *Server) listProfiles(w http.ResponseWriter, r *http.Request) {
	filter, err := parseListQuery(r)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	profiles, total, err := s.store.List(filter)
	if err != nil {
		log.Printf("store.List: %v", err)
		writeError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	writeJSON(w, http.StatusOK, listResponse{
		Status: "success",
		Page:   filter.Page,
		Limit:  filter.Limit,
		Total:  total,
		Data:   profiles,
	})
}

// --- GET /api/profiles/search ---

func (s *Server) searchProfiles(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if strings.TrimSpace(q) == "" {
		writeError(w, http.StatusBadRequest, "Query is required")
		return
	}

	parsed := ParseNL(q, s.codeToName)
	if !parsed.Matched {
		writeError(w, http.StatusUnprocessableEntity, "Unable to interpret query")
		return
	}

	// Pagination + sort from the query string override defaults.
	page, limit, err := parsePageLimit(r)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	sortBy, order, err := parseSort(r)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	f := parsed.Filter
	f.Page = page
	f.Limit = limit
	f.SortBy = sortBy
	f.Order = order

	profiles, total, err := s.store.List(f)
	if err != nil {
		log.Printf("store.List: %v", err)
		writeError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	writeJSON(w, http.StatusOK, listResponse{
		Status: "success",
		Page:   f.Page,
		Limit:  f.Limit,
		Total:  total,
		Data:   profiles,
	})
}

// --- Query parsing helpers ---

// Any malformed value returns the generic "Invalid query parameters" error
// per the spec.
var errInvalidQuery = errors.New("Invalid query parameters")

func parseListQuery(r *http.Request) (ListFilter, error) {
	q := r.URL.Query()
	f := ListFilter{}

	if v := strings.TrimSpace(q.Get("gender")); v != "" {
		lv := strings.ToLower(v)
		if lv != "male" && lv != "female" {
			return f, errInvalidQuery
		}
		f.Gender = lv
	}
	if v := strings.TrimSpace(q.Get("age_group")); v != "" {
		lv := strings.ToLower(v)
		if _, ok := validAgeGroups[lv]; !ok {
			return f, errInvalidQuery
		}
		f.AgeGroup = lv
	}
	if v := strings.TrimSpace(q.Get("country_id")); v != "" {
		if len(v) != 2 || !isAlpha(v) {
			return f, errInvalidQuery
		}
		f.CountryID = strings.ToUpper(v)
	}
	if v := strings.TrimSpace(q.Get("min_age")); v != "" {
		n, err := parseNonNegInt(v)
		if err != nil {
			return f, errInvalidQuery
		}
		f.MinAge = &n
	}
	if v := strings.TrimSpace(q.Get("max_age")); v != "" {
		n, err := parseNonNegInt(v)
		if err != nil {
			return f, errInvalidQuery
		}
		f.MaxAge = &n
	}
	if v := strings.TrimSpace(q.Get("min_gender_probability")); v != "" {
		fv, err := parseProb(v)
		if err != nil {
			return f, errInvalidQuery
		}
		f.MinGenderProb = &fv
	}
	if v := strings.TrimSpace(q.Get("min_country_probability")); v != "" {
		fv, err := parseProb(v)
		if err != nil {
			return f, errInvalidQuery
		}
		f.MinCountryProb = &fv
	}

	sortBy, order, err := parseSort(r)
	if err != nil {
		return f, err
	}
	f.SortBy = sortBy
	f.Order = order

	page, limit, err := parsePageLimit(r)
	if err != nil {
		return f, err
	}
	f.Page = page
	f.Limit = limit

	return f, nil
}

func parseSort(r *http.Request) (sortBy, order string, err error) {
	q := r.URL.Query()
	sortBy = strings.TrimSpace(q.Get("sort_by"))
	if sortBy != "" {
		if _, ok := sortColumns[sortBy]; !ok {
			return "", "", errInvalidQuery
		}
	}
	order = strings.ToLower(strings.TrimSpace(q.Get("order")))
	if order != "" && order != "asc" && order != "desc" {
		return "", "", errInvalidQuery
	}
	return sortBy, order, nil
}

func parsePageLimit(r *http.Request) (page, limit int, err error) {
	q := r.URL.Query()
	page = 1
	limit = 10

	if v := strings.TrimSpace(q.Get("page")); v != "" {
		n, e := strconv.Atoi(v)
		if e != nil || n < 1 {
			return 0, 0, errInvalidQuery
		}
		page = n
	}
	if v := strings.TrimSpace(q.Get("limit")); v != "" {
		n, e := strconv.Atoi(v)
		if e != nil || n < 1 {
			return 0, 0, errInvalidQuery
		}
		if n > 50 {
			n = 50
		}
		limit = n
	}
	return page, limit, nil
}

var validAgeGroups = map[string]struct{}{
	"child":    {},
	"teenager": {},
	"adult":    {},
	"senior":   {},
}

func isAlpha(s string) bool {
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return false
		}
	}
	return true
}

func parseNonNegInt(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, err
	}
	if n < 0 {
		return 0, errInvalidQuery
	}
	return n, nil
}

func parseProb(s string) (float64, error) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if f < 0 || f > 1 {
		return 0, errInvalidQuery
	}
	return f, nil
}
