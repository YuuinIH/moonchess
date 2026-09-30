package control_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/yuuinih/moonchess/internal/control"
)

func TestPostgresPlaneFencesExpiredOwner(t *testing.T) {
	dsn := os.Getenv("MOONCHESS_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set MOONCHESS_TEST_POSTGRES_URL for the PostgreSQL contract test")
	}
	ctx := context.Background()
	schema := "contract_" + strings.ReplaceAll(time.Now().Format("150405000000"), ".", "")
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+identifier+" CASCADE")
	})
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	plane, err := control.OpenPostgres(ctx, parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	if err := plane.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	gameID := fmt.Sprintf("fencing-%d", time.Now().UnixNano())
	if _, err := plane.Create(ctx, gameID, "game/"+gameID+"/epoch/0/seq/0"); err != nil {
		t.Fatal(err)
	}

	leaseA, err := plane.Acquire(ctx, gameID, "worker-a", 120*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(180 * time.Millisecond)
	leaseB, err := plane.Acquire(ctx, gameID, "worker-b", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if leaseB.Epoch != leaseA.Epoch+1 {
		t.Fatalf("epoch = %d, want %d", leaseB.Epoch, leaseA.Epoch+1)
	}

	_, err = plane.Commit(ctx, leaseA, 0, "stale-snapshot")
	if !errors.Is(err, control.ErrFenced) {
		t.Fatalf("stale commit error = %v, want ErrFenced", err)
	}
	committed, err := plane.Commit(ctx, leaseB, 0, "fresh-snapshot")
	if err != nil {
		t.Fatal(err)
	}
	if committed.SnapshotSeq != 1 || committed.SnapshotKey != "fresh-snapshot" {
		t.Fatalf("committed = %#v", committed)
	}
}
