package store

import (
	"context"
	"database/sql"
)

// queryer is what eachRow needs: a *sql.DB or a *sql.Tx.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// eachRow runs query and calls scan once per row. It always closes the rows and
// returns the first error: from the query, from scan, or from iterating (a
// cancelled context, for example, would otherwise look like a short result).
func eachRow(ctx context.Context, q queryer, scan func(*sql.Rows) error, query string, args ...any) error {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}
