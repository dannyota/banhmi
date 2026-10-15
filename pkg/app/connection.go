package app

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// openDatabasePool forces the lazy pool's first acquire through the boundary.
func openDatabasePool(ctx context.Context, uri string, open func(context.Context, string) (*pgxpool.Pool, error)) (*pgxpool.Pool, error) {
	pool, err := open(ctx, uri)
	if err != nil {
		return nil, sanitizeConnectionError(err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, sanitizeConnectionError(err)
	}
	return pool, nil
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
