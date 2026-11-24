package session

import (
	"testing"
)

func Test_historyRange(t *testing.T) {
	cases := []struct {
		name                  string
		total, page, pageSize int
		wantOffset, wantLimit int
	}{
		{"all", 25, 0, 0, 0, 25},
		{"latest page", 25, 1, 10, 15, 10},
		{"middle page", 25, 2, 10, 5, 10},
		{"last partial page", 25, 3, 10, 0, 5},
		{"beyond", 25, 4, 10, 0, 0},
		{"page default", 25, 0, 10, 15, 10},
		{"page size capped", 300, 1, 1000, 200, maxHistoryPageSize},
		{"empty", 0, 1, 10, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			offset, limit := historyRange(c.total, c.page, c.pageSize)
			if offset != c.wantOffset || limit != c.wantLimit {
				t.Fatalf("got (%d,%d), want (%d,%d)", offset, limit, c.wantOffset, c.wantLimit)
			}
		})
	}
}
