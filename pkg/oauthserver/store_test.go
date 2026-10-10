package oauthserver

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNewStoreRejectsMissingDependencies(t *testing.T) {
	tests := []struct {
		name string
		pool bool
		clients bool
		want string
	}{
		{name: "pool", want: "pool"},
		{name: "clients", pool: true, want: "client registry"},
		{name: "strategy", pool: true, clients: true, want: "strategy"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var store *Store
			var err error
			switch {
			case !tt.pool:
				store, err = NewStore(nil, nil, nil)
			case !tt.clients:
				store, err = NewStore(&pgxpool.Pool{}, nil, nil)
			default:
				store, err = NewStore(&pgxpool.Pool{}, &ClientRegistry{}, nil)
			}
			if err == nil || store != nil {
				t.Fatalf("NewStore() = (%v, %v), want nil store and error", store, err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("NewStore() error = %q, want %q", err, tt.want)
			}
		})
	}
}

