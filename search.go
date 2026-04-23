package main

import (
	"regexp"
	"strconv"
	"strings"
)

// Pre-compiled regexes for the rule-based NL parser.
// Word boundaries keep e.g. "male" from matching inside "female".
var (
	reMale   = regexp.MustCompile(`\b(males?|men|boys?|guys?|gentlemen)\b`)
	reFemale = regexp.MustCompile(`\b(females?|women|girls?|ladies|ladys)\b`)

	// Inclusive age bounds.
	reAgeAbove = regexp.MustCompile(`\b(?:above|over|older\s+than|greater\s+than|at\s+least|>=?)\s*(\d+)`)
	reAgeBelow = regexp.MustCompile(`\b(?:below|under|younger\s+than|less\s+than|at\s+most|<=?)\s*(\d+)`)

	reBetween = regexp.MustCompile(`\bbetween\s+(\d+)\s+and\s+(\d+)`)
	reAgeRange = regexp.MustCompile(`\b(\d+)\s*(?:-|to)\s*(\d+)\b`)

	// "aged 42", "age 42"
	reAgedExact = regexp.MustCompile(`\b(?:aged?|age)\s+(\d+)\b`)

	// ISO-2 country code at end or surrounded by boundaries (e.g. "country NG").
	reISOCode = regexp.MustCompile(`\b([A-Z]{2})\b`)
)

// ageGroupAliases maps every token we recognise to the canonical stored group.
var ageGroupAliases = map[string]string{
	"child":     "child",
	"children":  "child",
	"kid":       "child",
	"kids":      "child",
	"teenager":  "teenager",
	"teenagers": "teenager",
	"teen":      "teenager",
	"teens":     "teenager",
	"adult":     "adult",
	"adults":    "adult",
	"senior":    "senior",
	"seniors":   "senior",
	"elder":     "senior",
	"elders":    "senior",
	"elderly":   "senior",
}

// countryAliases handles common everyday names not in the ISO name list.
var countryAliases = map[string]string{
	"usa":               "US",
	"u.s.a":             "US",
	"u.s.a.":            "US",
	"u.s.":              "US",
	"us":                "US",
	"america":           "US",
	"uk":                "GB",
	"u.k.":              "GB",
	"britain":           "GB",
	"great britain":     "GB",
	"england":           "GB",
	"ivory coast":       "CI",
	"drc":               "CD",
	"congo kinshasa":    "CD",
	"congo brazzaville": "CG",
	"sao tome":          "ST",
	"cape verde":        "CV",
	"swaziland":         "SZ",
}

// ParsedQuery is the structured result of a NL parse. matched is false when
// no recognised token was found — callers return the "Unable to interpret"
// error in that case.
type ParsedQuery struct {
	Filter  ListFilter
	Matched bool
}

// ParseNL turns a plain-English query into a ListFilter (without pagination).
// Parsing is rule-based: tokenise the input, then look for gender, age group,
// age bounds, and country references.
//
// countryNameToCode maps ISO-2 code → canonical English name. It's iterated in
// longest-name-first order so "Central African Republic" wins over "Africa".
func ParseNL(q string, codeToName map[string]string) ParsedQuery {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return ParsedQuery{}
	}

	f := ListFilter{}
	matched := false

	// --- Gender ---
	hasMale := reMale.MatchString(q)
	hasFemale := reFemale.MatchString(q)
	switch {
	case hasMale && !hasFemale:
		f.Gender = "male"
		matched = true
	case hasFemale && !hasMale:
		f.Gender = "female"
		matched = true
	case hasMale && hasFemale:
		// Both genders mentioned → no gender filter, but query was still parsed.
		matched = true
	}

	// --- Age group (longest alias first so "teenagers" beats "teen") ---
	aliases := sortByLenDesc(keys(ageGroupAliases))
	for _, alias := range aliases {
		re := regexp.MustCompile(`\b` + regexp.QuoteMeta(alias) + `\b`)
		if re.MatchString(q) {
			f.AgeGroup = ageGroupAliases[alias]
			matched = true
			break
		}
	}

	// --- Age bounds ---
	// Explicit bounds win over the "young" / "old" shorthands.
	if m := reBetween.FindStringSubmatch(q); m != nil {
		lo, _ := strconv.Atoi(m[1])
		hi, _ := strconv.Atoi(m[2])
		if lo > hi {
			lo, hi = hi, lo
		}
		f.MinAge = intPtr(lo)
		f.MaxAge = intPtr(hi)
		matched = true
	} else if m := reAgeRange.FindStringSubmatch(q); m != nil {
		lo, _ := strconv.Atoi(m[1])
		hi, _ := strconv.Atoi(m[2])
		if lo > hi {
			lo, hi = hi, lo
		}
		f.MinAge = intPtr(lo)
		f.MaxAge = intPtr(hi)
		matched = true
	}

	if m := reAgeAbove.FindStringSubmatch(q); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			f.MinAge = intPtr(n)
			matched = true
		}
	}
	if m := reAgeBelow.FindStringSubmatch(q); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			f.MaxAge = intPtr(n)
			matched = true
		}
	}

	// "young" → 16-24, only if no explicit bound already set.
	if containsWord(q, "young") {
		if f.MinAge == nil {
			f.MinAge = intPtr(16)
		}
		if f.MaxAge == nil {
			f.MaxAge = intPtr(24)
		}
		matched = true
	}
	// "old" (but not "older than N" — that case is already handled) maps to 60+.
	if containsWord(q, "old") && f.MinAge == nil {
		f.MinAge = intPtr(60)
		matched = true
	}

	// "aged 42" / "age 42" — exact age, only if no bound yet.
	if m := reAgedExact.FindStringSubmatch(q); m != nil && f.MinAge == nil && f.MaxAge == nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			f.MinAge = intPtr(n)
			f.MaxAge = intPtr(n)
			matched = true
		}
	}

	// --- Country ---
	// 1. Try aliases (longest first).
	code := ""
	aliasList := sortByLenDesc(keys(countryAliases))
	for _, a := range aliasList {
		if containsPhrase(q, a) {
			code = countryAliases[a]
			break
		}
	}
	// 2. Try full country names from the seed map (longest first).
	if code == "" {
		names := make([]string, 0, len(codeToName))
		lowerToCode := make(map[string]string, len(codeToName))
		for c, n := range codeToName {
			ln := strings.ToLower(n)
			names = append(names, ln)
			lowerToCode[ln] = c
		}
		names = sortByLenDesc(names)
		for _, n := range names {
			if containsPhrase(q, n) {
				code = lowerToCode[n]
				break
			}
		}
	}
	// 3. Fall back to an explicit 2-letter ISO code token (match on original case).
	if code == "" {
		if m := reISOCode.FindStringSubmatch(strings.ToUpper(q)); m != nil {
			if _, ok := codeToName[m[1]]; ok {
				code = m[1]
			}
		}
	}
	if code != "" {
		f.CountryID = code
		matched = true
	}

	// Validity sanity check on explicit numeric bounds.
	if f.MinAge != nil && f.MaxAge != nil && *f.MinAge > *f.MaxAge {
		// Nonsensical; drop the query.
		return ParsedQuery{}
	}

	return ParsedQuery{Filter: f, Matched: matched}
}

// --- small helpers ---

func intPtr(n int) *int { return &n }

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// sortByLenDesc sorts in place using a simple insertion sort — the inputs are
// small (tens of items) so a hand-rolled sort avoids importing the sort pkg.
func sortByLenDesc(xs []string) []string {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && len(xs[j]) > len(xs[j-1]); j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
	return xs
}

func containsWord(s, word string) bool {
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(word) + `\b`)
	return re.MatchString(s)
}

// containsPhrase matches a multi-word phrase with word boundaries on each end.
// Multi-word country names like "south africa" still work.
func containsPhrase(s, phrase string) bool {
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(phrase) + `\b`)
	return re.MatchString(s)
}
