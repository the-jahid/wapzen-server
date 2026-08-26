package phonenumbers

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type recordingDB struct {
	query string
	args  []any
	rows  pgx.Rows
}

func (db *recordingDB) Query(_ context.Context, query string, args ...any) (pgx.Rows, error) {
	db.query = query
	db.args = args
	if db.rows == nil {
		panic("Query was not expected")
	}
	return db.rows, nil
}

func (db *recordingDB) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	db.query = query
	db.args = args
	return errRow{err: pgx.ErrNoRows}
}

func (db *recordingDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	panic("Exec was not expected")
}

type errRow struct {
	err error
}

type emptyRows struct{}

func (*emptyRows) Close()                                       {}
func (*emptyRows) Err() error                                   { return nil }
func (*emptyRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (*emptyRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (*emptyRows) Next() bool                                   { return false }
func (*emptyRows) Scan(...any) error                            { panic("Scan was not expected") }
func (*emptyRows) Values() ([]any, error)                       { panic("Values was not expected") }
func (*emptyRows) RawValues() [][]byte                          { return nil }
func (*emptyRows) Conn() *pgx.Conn                              { return nil }

func (r errRow) Scan(...any) error {
	return r.err
}

func TestGetByUserScopesByAuthenticatedUser(t *testing.T) {
	db := &recordingDB{}
	repo := &Repository{db: db}

	_, err := repo.GetByUser(context.Background(), "user_123", "phone_123")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if !strings.Contains(db.query, "WHERE id = $1 AND user_id = $2") {
		t.Fatalf("query does not scope by user: %s", db.query)
	}
	if got, want := db.args, []any{"phone_123", "user_123"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestListConnectedIsSystemScoped(t *testing.T) {
	db := &recordingDB{rows: &emptyRows{}}
	repo := &Repository{db: db}

	phoneNumbers, err := repo.ListConnected(context.Background())
	if err != nil {
		t.Fatalf("ListConnected() error = %v", err)
	}
	if phoneNumbers == nil || len(phoneNumbers) != 0 {
		t.Fatalf("ListConnected() = %#v, want non-nil empty slice", phoneNumbers)
	}
	if !strings.Contains(db.query, "WHERE status = $1") {
		t.Fatalf("query does not filter connected rows: %s", db.query)
	}
	if strings.Contains(db.query, "WHERE user_id") || strings.Contains(db.query, "AND user_id") {
		t.Fatalf("query unexpectedly scopes by user: %s", db.query)
	}
	if got, want := db.args, []any{"connected"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestMarkDisconnectedScopesByAuthenticatedUser(t *testing.T) {
	db := &recordingDB{}
	repo := &Repository{db: db}

	_, err := repo.MarkDisconnected(context.Background(), "user_123", "phone_123")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if !strings.Contains(db.query, "WHERE id = $1 AND user_id = $2") {
		t.Fatalf("query does not scope update by user: %s", db.query)
	}
	if got, want := db.args, []any{"phone_123", "user_123", "disconnected"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestResetPendingLoginScopesByAuthenticatedUser(t *testing.T) {
	db := &recordingDB{}
	repo := &Repository{db: db}

	_, err := repo.ResetPendingLogin(context.Background(), "user_123", "phone_123")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if !strings.Contains(db.query, "WHERE id = $1 AND user_id = $2") {
		t.Fatalf("query does not scope update by user: %s", db.query)
	}
	if got, want := db.args, []any{"phone_123", "user_123", "pending_qr"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestResetForRePairClearsJIDAndPreservesScopedRow(t *testing.T) {
	db := &recordingDB{}
	repo := &Repository{db: db}

	_, err := repo.ResetForRePair(context.Background(), "user_123", "phone_123")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if !strings.Contains(db.query, "SET wa_jid = NULL") {
		t.Fatalf("query does not clear the old WhatsApp JID: %s", db.query)
	}
	if strings.Contains(db.query, "DELETE FROM phone_numbers") {
		t.Fatalf("query unexpectedly deletes the phone-number row: %s", db.query)
	}
	if !strings.Contains(db.query, "WHERE id = $1 AND user_id = $2") {
		t.Fatalf("query does not scope update by user: %s", db.query)
	}
	if got, want := db.args, []any{"phone_123", "user_123", "pending_qr"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestDeleteByUserScopesByAuthenticatedUser(t *testing.T) {
	db := &recordingDB{}
	repo := &Repository{db: db}

	_, err := repo.DeleteByUser(context.Background(), "user_123", "phone_123")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if !strings.Contains(db.query, "DELETE FROM phone_numbers") {
		t.Fatalf("query does not delete phone number: %s", db.query)
	}
	if !strings.Contains(db.query, "WHERE id = $1 AND user_id = $2") {
		t.Fatalf("query does not scope delete by user: %s", db.query)
	}
	if got, want := db.args, []any{"phone_123", "user_123"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}
