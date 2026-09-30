package control_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/yuuinih/moonchess/internal/control"
)

func TestEtcdFencesExpiredOwner(t *testing.T) {
	endpoint := os.Getenv("MOONCHESS_TEST_ETCD_ENDPOINT")
	if endpoint == "" {
		t.Skip("set MOONCHESS_TEST_ETCD_ENDPOINT")
	}
	p, err := control.OpenEtcd([]string{endpoint})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx := context.Background()
	id := "fencing-" + time.Now().Format("20060102150405.000000000")
	r, err := p.Create(ctx, id, "checkpoint-in-mooncake")
	if err != nil {
		t.Fatal(err)
	}
	a, err := p.Acquire(ctx, r, "a", 1)
	if err != nil {
		t.Fatal(err)
	}
	owned, err := p.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if owned.OwnerID != "a" || owned.Epoch != 1 {
		t.Fatalf("first owner: %#v", owned)
	}
	// Revoke through lease expiry so an unchanged process has a genuinely stale token.
	time.Sleep(1800 * time.Millisecond)
	for attempt := 0; attempt < 20; attempt++ {
		r, err = p.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if r.OwnerID == "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if r.OwnerID != "" {
		t.Fatal("owner lease did not expire")
	}
	b, err := p.Acquire(ctx, r, "b", 3)
	if err != nil {
		t.Fatal(err)
	}
	if b.Epoch != a.Epoch+1 {
		t.Fatalf("epochs: %d -> %d", a.Epoch, b.Epoch)
	}
	if _, err := p.Commit(ctx, a, owned, "stale"); !errors.Is(err, control.ErrFenced) {
		t.Fatalf("stale commit: %v", err)
	}
	fresh, err := p.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := p.Commit(ctx, b, fresh, "delta-1")
	if err != nil {
		t.Fatal(err)
	}
	if committed.HeadRef != "delta-1" || committed.CommittedSeq != 1 {
		t.Fatalf("commit: %#v", committed)
	}
	migrating, err := p.BeginMigration(ctx, b, committed, "c")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Commit(ctx, b, migrating, "delta-2"); !errors.Is(err, control.ErrFenced) {
		t.Fatalf("commit after migration began: %v", err)
	}
}
