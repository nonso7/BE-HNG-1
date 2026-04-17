package main

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Server struct {
	store *Store
}

func NewServer(store *Store) *Server { return &Server{store: store} }

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/profiles", s.createProfile)
	mux.HandleFunc("GET /api/profiles", s.listProfiles)
	mux.HandleFunc("GET /api/profiles/{id}", s.getProfile)
	mux.HandleFunc("DELETE /api/profiles/{id}", s.deleteProfile)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{
			"status":  "success",
			"message": "HNG Stage 1 Profile API. See /api/profiles",
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

// createProfile implements POST /api/profiles.
func (s *Server) createProfile(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	// Decode into a loose map to distinguish "missing" from "wrong type".
	var raw map[string]json.RawMessage
	if len(body) == 0 {
		writeError(w, http.StatusBadRequest, "Name is required")
		return
	}
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

	// Idempotency check — if a profile with this name already exists, return it.
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

	// Call the three upstream APIs. Any invalid response → 502 and no storage.
	gender, genderProb, sampleSize, err := fetchGenderize(name)
	if err != nil {
		if ue, ok := err.(*upstreamError); ok {
			writeError(w, http.StatusBadGateway, ue.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "Genderize returned an invalid response")
		return
	}
	age, err := fetchAgify(name)
	if err != nil {
		if ue, ok := err.(*upstreamError); ok {
			writeError(w, http.StatusBadGateway, ue.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "Agify returned an invalid response")
		return
	}
	countryID, countryProb, err := fetchNationalize(name)
	if err != nil {
		if ue, ok := err.(*upstreamError); ok {
			writeError(w, http.StatusBadGateway, ue.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "Nationalize returned an invalid response")
		return
	}

	id, err := uuid.NewV7()
	if err != nil {
		log.Printf("uuid.NewV7: %v", err)
		writeError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	profile := &Profile{
		ID:                 id.String(),
		Name:               name,
		Gender:             gender,
		GenderProbability:  genderProb,
		SampleSize:         sampleSize,
		Age:                age,
		AgeGroup:           classifyAge(age),
		CountryID:          countryID,
		CountryProbability: countryProb,
		CreatedAt:          time.Now().UTC(),
	}

	if err := s.store.Insert(profile); err != nil {
		// A concurrent request may have inserted the same name — handle race.
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

func (s *Server) listProfiles(w http.ResponseWriter, r *http.Request) {
	f := ListFilter{
		Gender:    r.URL.Query().Get("gender"),
		CountryID: r.URL.Query().Get("country_id"),
		AgeGroup:  r.URL.Query().Get("age_group"),
	}
	profiles, err := s.store.List(f)
	if err != nil {
		log.Printf("store.List: %v", err)
		writeError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	writeJSON(w, http.StatusOK, listResponse{
		Status: "success",
		Count:  len(profiles),
		Data:   profiles,
	})
}

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
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusNoContent)
}
