package main

import (
	"fmt"
	"net/url"
)

type paginationLinks struct {
	Self string  `json:"self"`
	Next *string `json:"next"`
	Prev *string `json:"prev"`
}

func buildPagination(basePath string, query url.Values, page, limit, total int) (int, paginationLinks) {
	totalPages := 0
	if limit > 0 {
		totalPages = (total + limit - 1) / limit
	}
	mk := func(p int) string {
		q := url.Values{}
		for k, vs := range query {
			if k == "page" || k == "limit" {
				continue
			}
			for _, v := range vs {
				q.Add(k, v)
			}
		}
		q.Set("page", fmt.Sprint(p))
		q.Set("limit", fmt.Sprint(limit))
		return basePath + "?" + q.Encode()
	}
	links := paginationLinks{Self: mk(page)}
	if page < totalPages {
		s := mk(page + 1)
		links.Next = &s
	}
	if page > 1 && totalPages > 0 {
		s := mk(page - 1)
		links.Prev = &s
	}
	return totalPages, links
}
