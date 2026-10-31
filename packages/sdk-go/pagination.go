package emailplatform

import (
	"errors"
	"iter"
)

var (
	errRepeatedCursor = errors.New("emailplatform: the API returned a cursor seen before in this listing")
	errEmptyCursor    = errors.New("emailplatform: the API returned an empty cursor")
)

// paginate yields the items of every page, starting again at the first
// page on every range. fetch gets nil for the first page.
func paginate[T any](fetch func(cursor *string) ([]T, *string, error)) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var cursor *string
		seen := map[string]bool{}
		for {
			items, next, err := fetch(cursor)
			if err != nil {
				var zero T
				yield(zero, err)
				return
			}
			for _, item := range items {
				if !yield(item, nil) {
					return
				}
			}
			if next == nil {
				return
			}
			var zero T
			switch {
			case *next == "":
				yield(zero, errEmptyCursor)
				return
			case seen[*next]:
				yield(zero, errRepeatedCursor)
				return
			}
			seen[*next] = true
			cursor = next
		}
	}
}
