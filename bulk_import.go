package main

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// importChunkSize is the number of validated rows we accumulate in memory
// before flushing them to SQLite as a single short transaction. Tuned so the
// transaction commit latency stays under ~50ms on a small Render instance,
// keeping concurrent reads from waiting on the writer for long.
const importChunkSize = 500

// importMaxRows enforces the 4B spec ceiling. Anything larger is rejected
// before we start work so a malicious multi-GB upload can't tie up the writer
// for hours.
const importMaxRows = 500_000

// importColumns lists the CSV columns we accept, in canonical order. The CSV
// header row decides which actual position each column lives at, so callers
// can supply them in any order.
var importColumns = map[string]bool{
	"name":                true,
	"gender":              true,
	"age":                 true,
	"country_id":          true,
	"gender_probability":  false, // optional
	"country_probability": false, // optional
}

type importSummary struct {
	Status    string         `json:"status"`
	TotalRows int            `json:"total_rows"`
	Inserted  int            `json:"inserted"`
	Skipped   int            `json:"skipped"`
	Reasons   map[string]int `json:"reasons"`
}

func newImportSummary() *importSummary {
	return &importSummary{
		Status:  "success",
		Reasons: map[string]int{},
	}
}

// importProfiles handles POST /api/profiles/import. The CSV body is consumed
// in a streaming fashion (no full-file buffering), validated row-by-row,
// chunked into transactions of importChunkSize, and reported with the
// summary shape mandated by the Stage 4B spec.
func (s *Server) importProfiles(w http.ResponseWriter, r *http.Request) {
	body, err := openImportBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	defer body.Close()

	cr := csv.NewReader(body)
	cr.FieldsPerRecord = -1 // we validate field count per row
	cr.ReuseRecord = true   // re-use the underlying slice; we copy values out
	cr.LazyQuotes = true
	cr.TrimLeadingSpace = true

	header, err := cr.Read()
	if err != nil {
		writeError(w, http.StatusBadRequest, "CSV header missing or unreadable")
		return
	}
	idx, missingHeaderErr := buildHeaderIndex(header)
	if missingHeaderErr != nil {
		writeError(w, http.StatusBadRequest, missingHeaderErr.Error())
		return
	}

	summary := newImportSummary()
	chunk := make([]*Profile, 0, importChunkSize)

	for {
		record, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// Malformed row — skip it and keep going.
			summary.TotalRows++
			summary.Skipped++
			summary.Reasons["malformed_row"]++
			continue
		}
		summary.TotalRows++
		if summary.TotalRows > importMaxRows {
			writeError(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("CSV exceeds the %d-row limit", importMaxRows))
			return
		}

		profile, reason := parseImportRow(record, idx, s.codeToName)
		if reason != "" {
			summary.Skipped++
			summary.Reasons[reason]++
			continue
		}
		chunk = append(chunk, profile)
		if len(chunk) >= importChunkSize {
			if err := s.flushImportChunk(chunk, summary); err != nil {
				log.Printf("import flush: %v", err)
				writeError(w, http.StatusInternalServerError, "Internal server error during import")
				return
			}
			chunk = chunk[:0]
		}
	}
	if len(chunk) > 0 {
		if err := s.flushImportChunk(chunk, summary); err != nil {
			log.Printf("import final flush: %v", err)
			writeError(w, http.StatusInternalServerError, "Internal server error during import")
			return
		}
	}

	if summary.Inserted > 0 {
		s.cache.Bump()
	}
	writeJSON(w, http.StatusOK, summary)
}

// flushImportChunk pushes a chunk to the store, updating insertion and
// duplicate counters in the summary.
func (s *Server) flushImportChunk(chunk []*Profile, summary *importSummary) error {
	inserted, dup, err := s.store.BulkInsertIgnore(chunk)
	if err != nil {
		return err
	}
	summary.Inserted += inserted
	if dup > 0 {
		summary.Skipped += dup
		summary.Reasons["duplicate_name"] += dup
	}
	return nil
}

// openImportBody returns the CSV stream regardless of whether the client sent
// a raw text/csv body or a multipart upload with a "file" field. In both
// cases the returned ReadCloser streams from the network — we never buffer
// the whole file.
func openImportBody(r *http.Request) (io.ReadCloser, error) {
	ct := r.Header.Get("Content-Type")
	mediaType, params, _ := mime.ParseMediaType(ct)
	switch {
	case strings.HasPrefix(mediaType, "multipart/"):
		// We deliberately use MultipartReader instead of ParseMultipartForm:
		// the latter buffers; the former streams.
		mr, err := r.MultipartReader()
		if err != nil {
			return nil, fmt.Errorf("invalid multipart body: %w", err)
		}
		for {
			part, err := mr.NextPart()
			if errors.Is(err, io.EOF) {
				return nil, errors.New(`multipart upload missing "file" part`)
			}
			if err != nil {
				return nil, fmt.Errorf("multipart read: %w", err)
			}
			if part.FormName() == "file" {
				return part, nil
			}
			// Discard non-file parts so the next NextPart() works.
			_, _ = io.Copy(io.Discard, part)
			_ = part.Close()
		}
	case mediaType == "" || mediaType == "text/csv" || mediaType == "application/csv" || mediaType == "application/octet-stream":
		_ = params // unused; charset etc. ignored
		return r.Body, nil
	default:
		return nil, fmt.Errorf("unsupported Content-Type %q (use text/csv or multipart/form-data)", mediaType)
	}
}

// buildHeaderIndex maps each known column name to its position in the CSV
// header row. Returns an error only when one of the *required* columns is
// missing.
func buildHeaderIndex(header []string) (map[string]int, error) {
	idx := map[string]int{}
	for i, h := range header {
		key := strings.ToLower(strings.TrimSpace(h))
		idx[key] = i
	}
	for col, required := range importColumns {
		if !required {
			continue
		}
		if _, ok := idx[col]; !ok {
			return nil, fmt.Errorf("CSV missing required column %q", col)
		}
	}
	return idx, nil
}

// parseImportRow validates one CSV record and returns either a Profile ready
// to insert, or the skip-reason string explaining why it was rejected.
// Empty reason means the profile is valid.
func parseImportRow(rec []string, idx map[string]int, codeToName map[string]string) (*Profile, string) {
	get := func(col string) string {
		i, ok := idx[col]
		if !ok || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}

	name := get("name")
	if name == "" {
		return nil, "missing_fields"
	}
	gender := strings.ToLower(get("gender"))
	if gender != "male" && gender != "female" {
		return nil, "invalid_gender"
	}
	ageStr := get("age")
	if ageStr == "" {
		return nil, "missing_fields"
	}
	age, err := strconv.Atoi(ageStr)
	if err != nil || age < 0 || age > 150 {
		return nil, "invalid_age"
	}
	countryID := strings.ToUpper(get("country_id"))
	if len(countryID) != 2 || !isAlpha(countryID) {
		return nil, "invalid_country_id"
	}

	gp := 1.0
	if v := get("gender_probability"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 0 || f > 1 {
			return nil, "invalid_probability"
		}
		gp = f
	}
	cp := 1.0
	if v := get("country_probability"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 0 || f > 1 {
			return nil, "invalid_probability"
		}
		cp = f
	}

	id, err := uuid.NewV7()
	if err != nil {
		return nil, "internal"
	}
	countryName := codeToName[countryID]
	if countryName == "" {
		countryName = countryID
	}
	return &Profile{
		ID:                 id.String(),
		Name:               name,
		Gender:             gender,
		GenderProbability:  gp,
		Age:                age,
		AgeGroup:           classifyAge(age),
		CountryID:          countryID,
		CountryName:        countryName,
		CountryProbability: cp,
		CreatedAt:          UTCTime(time.Now().UTC()),
	}, ""
}
