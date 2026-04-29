package main

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	reMale   = regexp.MustCompile(`\b(males?|men|boys?|guys?|gentlemen)\b`)
	reFemale = regexp.MustCompile(`\b(females?|women|girls?|ladies|ladys)\b`)

	reAgeAbove = regexp.MustCompile(`\b(?:above|over|older\s+than|greater\s+than|at\s+least|>=?)\s*(\d+)`)
	reAgeBelow = regexp.MustCompile(`\b(?:below|under|younger\s+than|less\s+than|at\s+most|<=?)\s*(\d+)`)

	reBetween  = regexp.MustCompile(`\bbetween\s+(\d+)\s+and\s+(\d+)`)
	reAgeRange = regexp.MustCompile(`\b(\d+)\s*(?:-|to)\s*(\d+)\b`)

	reAgedExact = regexp.MustCompile(`\b(?:aged?|age)\s+(\d+)\b`)

	reISOCode = regexp.MustCompile(`\b([A-Z]{2})\b`)
)

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

type ParsedQuery struct {
	Filter  ListFilter
	Matched bool
}

func ParseNL(q string, codeToName map[string]string) ParsedQuery {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return ParsedQuery{}
	}

	f := ListFilter{}
	matched := false

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
		matched = true
	}

	aliases := sortByLenDesc(keys(ageGroupAliases))
	for _, alias := range aliases {
		re := regexp.MustCompile(`\b` + regexp.QuoteMeta(alias) + `\b`)
		if re.MatchString(q) {
			f.AgeGroup = ageGroupAliases[alias]
			matched = true
			break
		}
	}

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

	if containsWord(q, "young") {
		if f.MinAge == nil {
			f.MinAge = intPtr(16)
		}
		if f.MaxAge == nil {
			f.MaxAge = intPtr(24)
		}
		matched = true
	}
	if containsWord(q, "old") && f.MinAge == nil {
		f.MinAge = intPtr(60)
		matched = true
	}

	if m := reAgedExact.FindStringSubmatch(q); m != nil && f.MinAge == nil && f.MaxAge == nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			f.MinAge = intPtr(n)
			f.MaxAge = intPtr(n)
			matched = true
		}
	}

	code := ""
	aliasList := sortByLenDesc(keys(countryAliases))
	for _, a := range aliasList {
		if containsPhrase(q, a) {
			code = countryAliases[a]
			break
		}
	}
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

	if f.MinAge != nil && f.MaxAge != nil && *f.MinAge > *f.MaxAge {
		return ParsedQuery{}
	}

	return ParsedQuery{Filter: f, Matched: matched}
}

func intPtr(n int) *int { return &n }

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

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

func containsPhrase(s, phrase string) bool {
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(phrase) + `\b`)
	return re.MatchString(s)
}
