package main

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// failingConnector never dials a database.
type failingConnector struct{ cause error }

func (c failingConnector) Connect(context.Context) (driver.Conn, error) { return nil, c.cause }
func (c failingConnector) Driver() driver.Driver                       { return nil }

func TestRawURIConnectionBoundary(t *testing.T) {
	for i, cause := range connectionCauses() {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Run("creation", func(t *testing.T) {
				db, err := openDatabase(t.Context(), "synthetic URI", func(string, string) (*sql.DB, error) {
					return nil, cause
				})
				if db != nil {
					t.Fatal("failed boundary returned a database")
				}
				assertSafeConnectionFailure(t, err, cause)
			})
			t.Run("first acquire and ping", func(t *testing.T) {
				open := func(string, string) (*sql.DB, error) {
					return sql.OpenDB(failingConnector{cause: cause}), nil
				}
				db, err := openDatabase(t.Context(), "synthetic URI", open)
				if db != nil {
					t.Fatal("failed boundary returned a database")
				}
				assertSafeConnectionFailure(t, err, cause)
			})
			t.Run("pinned connection", func(t *testing.T) {
				db := sql.OpenDB(failingConnector{cause: cause})
				defer func() { _ = db.Close() }()
				err := migrate(t.Context(), db, chainSpec{}, options{}, io.Discard)
				assertSafeConnectionFailure(t, err, cause)
			})
		})
	}
}

func assertSafeConnectionFailure(t *testing.T, err, cause error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected connection failure")
	}
	var output bytes.Buffer
	slog.New(slog.NewTextHandler(&output, nil)).Error("operation failed", "err", err)
	for _, text := range []string{err.Error(), output.String(), fmt.Sprintf("%+v", err)} {
		if strings.Contains(text, "synthetic@tail") {
			t.Fatal("credential fragment leaked")
		}
		if !strings.Contains(text, "connect corpus migration database: connection failed") {
			t.Fatal("connection classification missing")
		}
	}
	for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, sentinel) != errors.Is(cause, sentinel) {
			t.Fatal("context classification changed")
		}
	}
	var original, safe *pgconn.PgError
	if errors.As(cause, &original) && original.Code == "28P01" {
		if !errors.As(err, &safe) || safe.Code != original.Code || safe.Message != "" || safe.Detail != "" {
			t.Fatal("SQLSTATE must survive without message or detail")
		}
		if !strings.Contains(output.String(), "28P01") {
			t.Fatal("SQLSTATE missing from output")
		}
	}
}

func connectionCauses() []error {
	fragment := errors.New("lookup synthetic@tail: connection refused")
	return []error{
		fragment,
		fmt.Errorf("synthetic@tail: %w", context.Canceled),
		fmt.Errorf("synthetic@tail: %w", context.DeadlineExceeded),
		fmt.Errorf("synthetic@tail: %w", errors.Join(context.Canceled, context.DeadlineExceeded)),
		&pgconn.PgError{Code: "28P01", Message: fragment.Error(), Detail: fragment.Error()},
		&pgconn.PgError{Code: "synthetic@tail", Message: fragment.Error()},
	}
}
