package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// canonicalFilter renders a ListFilter into a stable string that is byte-identical
// for any two filters with the same intent — case folded, whitespace trimmed,
// pointer-vs-zero distinguished, fields written in alphabetical key order.
//
// The result is deliberately readable so it can be eyeballed during debugging.
// It is also the basis for the cache key (see filterCacheKey) and for treating
// "Nigerian females 20–45" and "women aged 20-45 living in Nigeria" as the
// same query: both are parsed to the same ListFilter, and that ListFilter
// produces the same canonical string here.
func canonicalFilter(f ListFilter, prefix string) string {
	parts := make([]string, 0, 12)

	if v := strings.TrimSpace(strings.ToLower(f.Gender)); v != "" {
		parts = append(parts, "gender="+v)
	}
	if v := strings.TrimSpace(strings.ToUpper(f.CountryID)); v != "" {
		parts = append(parts, "country="+v)
	}
	if v := strings.TrimSpace(strings.ToLower(f.AgeGroup)); v != "" {
		parts = append(parts, "age_group="+v)
	}
	if f.MinAge != nil {
		parts = append(parts, fmt.Sprintf("min_age=%d", *f.MinAge))
	}
	if f.MaxAge != nil {
		parts = append(parts, fmt.Sprintf("max_age=%d", *f.MaxAge))
	}
	if f.MinGenderProb != nil {
		parts = append(parts, fmt.Sprintf("min_gprob=%s", trimFloat(*f.MinGenderProb)))
	}
	if f.MinCountryProb != nil {
		parts = append(parts, fmt.Sprintf("min_cprob=%s", trimFloat(*f.MinCountryProb)))
	}

	sortBy := strings.TrimSpace(f.SortBy)
	if sortBy == "" {
		sortBy = "created_at"
	}
	order := strings.ToLower(strings.TrimSpace(f.Order))
	if order != "asc" && order != "desc" {
		order = "asc"
	}
	page := f.Page
	if page < 1 {
		page = 1
	}
	limit := f.Limit
	if limit < 1 {
		limit = 10
	}

	parts = append(parts,
		"sort_by="+sortBy,
		"order="+order,
		fmt.Sprintf("page=%d", page),
		fmt.Sprintf("limit=%d", limit),
	)

	sort.Strings(parts)
	if prefix == "" {
		prefix = "list"
	}
	return prefix + ":" + strings.Join(parts, "&")
}

// filterCacheKey returns a fixed-length cache key derived from canonicalFilter.
// SHA-256 hex keeps Redis-compatible key shape if we ever swap the cache
// backend, and keeps in-memory keys to a uniform 16 bytes regardless of how
// large the filter expression got.
func filterCacheKey(f ListFilter, prefix string) string {
	c := canonicalFilter(f, prefix)
	h := sha256.Sum256([]byte(c))
	return hex.EncodeToString(h[:8]) // first 8 bytes is plenty for in-process scope
}

// trimFloat formats a float without trailing zero noise so 0.5 and 0.50 collide.
func trimFloat(v float64) string {
	s := fmt.Sprintf("%.6f", v)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "" || s == "-" {
		s = "0"
	}
	return s
}
