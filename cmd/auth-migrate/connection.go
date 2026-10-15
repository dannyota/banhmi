package main

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

func openDatabase(ctx context.Context, uri string, open func(string, string) (*sql.DB, error)) (*sql.DB, error) {
	db, err := open("pgx", uri)
	if err != nil {
		return nil, sanitizeConnectionError(err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, sanitizeConnectionError(err)
	}
	return db, nil
}

// connectionFailure retains only safe classifications, never the driver's cause.
type connectionFailure struct {
	canceled bool
	deadline bool
	code     string
}

func (e *connectionFailure) Error() string {
	message := "connect auth database: connection failed"
	if e.code != "" {
		message += " (SQLSTATE " + e.code + ")"
	}
	if e.canceled {
		message += ": context canceled"
	}
	if e.deadline {
		message += ": context deadline exceeded"
	}
	return message
}

func (e *connectionFailure) Unwrap() []error {
	var causes []error
	if e.canceled {
		causes = append(causes, context.Canceled)
	}
	if e.deadline {
		causes = append(causes, context.DeadlineExceeded)
	}
	if e.code != "" {
		causes = append(causes, &pgconn.PgError{Code: e.code})
	}
	return causes
}

func sanitizeConnectionError(err error) error {
	failure := &connectionFailure{
		canceled: errors.Is(err, context.Canceled),
		deadline: errors.Is(err, context.DeadlineExceeded),
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && len(pgErr.Code) == 5 {
		valid := true
		for _, c := range pgErr.Code {
			if !(c >= '0' && c <= '9' || c >= 'A' && c <= 'Z') {
				valid = false
			}
		}
		if valid {
			failure.code = pgErr.Code
		}
	}
	return failure
}
