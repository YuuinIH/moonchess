package control

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/namespace"
)

func testLobby(t *testing.T) (*Etcd, context.Context) {
	t.Helper()
	endpoint := os.Getenv("MOONCHESS_TEST_ETCD_ENDPOINT")
	if endpoint == "" {
		t.Skip("set MOONCHESS_TEST_ETCD_ENDPOINT")
	}
	e, err := OpenEtcd([]string{endpoint})
	if err != nil {
		t.Fatal(err)
	}
	prefix := "/tests/lobby/" + t.Name() + "/"
	e.client.KV = namespace.NewKV(e.client.KV, prefix)
	t.Cleanup(func() { _, _ = e.client.Delete(context.Background(), "", clientv3.WithPrefix()); _ = e.Close() })
	return e, context.Background()
}
func TestAnonymousIdentity(t *testing.T) {
	e, ctx := testLobby(t)
	c, token, err := e.NewClient(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer e.client.Delete(ctx, clientKey(c.ID))
	if c.ID == "" || c.Nickname == "" {
		t.Fatal(c)
	}
	same, err := e.Authenticate(ctx, token)
	if err != nil || same.ID != c.ID {
		t.Fatal(same, err)
	}
	if _, err = e.Authenticate(ctx, c.ID+".forged"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal(err)
	}
	if err = e.Rename(ctx, same, "棋手"); err != nil {
		t.Fatal(err)
	}
	same, _ = e.Authenticate(ctx, token)
	if same.Nickname != "棋手" {
		t.Fatal(same)
	}
	if err = e.Enqueue(ctx, same); err != nil {
		t.Fatal(err)
	}
	if err = e.Reset(ctx, same); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Authenticate(ctx, token); !errors.Is(err, ErrUnauthorized) {
		t.Fatal(err)
	}
	if q, _ := e.Queued(ctx, same); q {
		t.Fatal("reset left a ticket")
	}
}
func TestConcurrentMatchUniquenessAndExpiredTickets(t *testing.T) {
	e, ctx := testLobby(t)
	other, err := OpenEtcd([]string{os.Getenv("MOONCHESS_TEST_ETCD_ENDPOINT")})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	other.client.KV = namespace.NewKV(other.client.KV, "/tests/lobby/"+t.Name()+"/")
	clients := map[string]string{}
	var cs []Client
	for i := 0; i < 12; i++ {
		c, token, err := e.NewClient(ctx)
		if err != nil {
			t.Fatal(err)
		}
		clients[c.ID] = token
		cs = append(cs, c)
		if err = e.Enqueue(ctx, c); err != nil {
			t.Fatal(err)
		}
		defer e.client.Delete(ctx, clientKey(c.ID))
		defer e.client.Delete(ctx, ticketKey(c.ID))
	}
	all, err := e.Tickets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var tickets []Ticket
	for _, ticket := range all {
		if _, ok := clients[ticket.ClientID]; ok {
			tickets = append(tickets, ticket)
		}
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	for i := 0; i < 6; i++ {
		for j := 0; j < 10; j++ {
			wg.Add(1)
			go func(i, j int) {
				defer wg.Done()
				p := e
				if j%2 == 1 {
					p = other
				}
				id := cs[i].ID + "-match-" + string(rune('a'+j))
				ok, err := p.Match(ctx, tickets[i*2], tickets[i*2+1], id, "test-checkpoint")
				if err != nil {
					t.Error(err)
				}
				if ok {
					defer e.client.Delete(ctx, "/moonchess/games/"+id+"/", clientv3.WithPrefix())
					mu.Lock()
					won++
					mu.Unlock()
				}
			}(i, j)
		}
	}
	wg.Wait()
	if won != 6 {
		t.Fatalf("successful matches=%d, want 6", won)
	}
	games := map[string]int{}
	for _, token := range clients {
		c, err := e.Authenticate(ctx, token)
		if err != nil || c.GameID == "" {
			t.Fatal(c, err)
		}
		games[c.GameID]++
		if q, _ := e.Queued(ctx, c); q {
			t.Fatal("matched client still queued")
		}
		if err = e.Enqueue(ctx, c); !errors.Is(err, ErrBusy) {
			t.Fatal("matched client requeued", err)
		}
	}
	for id, count := range games {
		if count != 2 {
			t.Fatalf("%s has %d clients", id, count)
		}
	}
	// A cancelled or expired ticket must fence a matcher holding the old ticket.
	a, _, _ := e.NewClient(ctx)
	b, _, _ := e.NewClient(ctx)
	defer e.client.Delete(ctx, clientKey(a.ID))
	defer e.client.Delete(ctx, clientKey(b.ID))
	defer e.client.Delete(ctx, ticketKey(b.ID))
	_ = e.Enqueue(ctx, a)
	_ = e.Enqueue(ctx, b)
	all, _ = e.Tickets(ctx)
	var ta, tb Ticket
	for _, v := range all {
		if v.ClientID == a.ID {
			ta = v
		}
		if v.ClientID == b.ID {
			tb = v
		}
	}
	_, err = e.client.Revoke(ctx, clientv3.LeaseID(ta.Lease))
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := e.Match(ctx, ta, tb, "expired-"+a.ID, "cp"); err != nil || ok {
		t.Fatal("expired ticket matched", ok, err)
	}
	_ = e.Cancel(ctx, b)
	if ok, err := e.Match(ctx, ta, tb, "cancelled-"+a.ID, "cp"); err != nil || ok {
		t.Fatal("cancelled ticket matched", ok, err)
	}
}
