package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/yuuinih/moonchess/internal/id"
	clientv3 "go.etcd.io/etcd/client/v3"
)

var ErrUnauthorized = errors.New("invalid anonymous session")

type Client struct {
	ID         string `json:"client_id"`
	Nickname   string `json:"nickname"`
	GameID     string `json:"game_id,omitempty"`
	SecretHash string `json:"-"`
	Revision   int64  `json:"-"`
}
type clientRecord struct {
	Client
	Hash string `json:"hash"`
}
type Ticket struct {
	ClientID string
	Revision int64
	Lease    int64
}
type Lobby interface {
	NewClient(context.Context) (Client, string, error)
	Authenticate(context.Context, string) (Client, error)
	Rename(context.Context, Client, string) error
	Reset(context.Context, Client) error
	Enqueue(context.Context, Client) error
	Cancel(context.Context, Client) error
	Queued(context.Context, Client) (bool, error)
	Tickets(context.Context) ([]Ticket, error)
	Match(context.Context, Ticket, Ticket, string, string) (bool, error)
	Leave(context.Context, Client) error
}

func clientKey(id string) string { return "/moonchess/clients/" + id }
func ticketKey(id string) string { return "/moonchess/tickets/" + id }
func hashSecret(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func (e *Etcd) NewClient(ctx context.Context) (Client, string, error) {
	name, err := id.RandomHex(16)
	if err != nil {
		return Client{}, "", err
	}
	secret, err := id.RandomHex(32)
	if err != nil {
		return Client{}, "", err
	}
	c := Client{ID: name, Nickname: "Moon-" + name[:6], SecretHash: hashSecret(secret)}
	resp, err := e.client.Txn(ctx).If(clientv3.Compare(clientv3.CreateRevision(clientKey(name)), "=", 0)).Then(clientv3.OpPut(clientKey(name), encode(clientRecord{c, c.SecretHash}))).Commit()
	if err != nil {
		return Client{}, "", err
	}
	if !resp.Succeeded {
		return Client{}, "", ErrBusy
	}
	c.Revision = resp.Header.Revision
	return c, name + "." + secret, nil
}
func (e *Etcd) readClient(ctx context.Context, name string) (Client, error) {
	resp, err := e.client.Get(ctx, clientKey(name))
	if err != nil {
		return Client{}, err
	}
	if len(resp.Kvs) == 0 {
		return Client{}, ErrUnauthorized
	}
	var r clientRecord
	if err = json.Unmarshal(resp.Kvs[0].Value, &r); err != nil {
		return Client{}, err
	}
	r.Client.SecretHash = r.Hash
	r.Client.Revision = resp.Kvs[0].ModRevision
	return r.Client, nil
}
func (e *Etcd) Authenticate(ctx context.Context, token string) (Client, error) {
	name, secret, ok := strings.Cut(token, ".")
	if !ok || len(name) != 32 || len(secret) != 64 {
		return Client{}, ErrUnauthorized
	}
	if _, err := hex.DecodeString(name); err != nil {
		return Client{}, ErrUnauthorized
	}
	c, err := e.readClient(ctx, name)
	if err != nil {
		return Client{}, err
	}
	if c.SecretHash != hashSecret(secret) {
		return Client{}, ErrUnauthorized
	}
	return c, nil
}
func (e *Etcd) updateClient(ctx context.Context, c Client, ops ...clientv3.Op) error {
	resp, err := e.client.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(clientKey(c.ID)), "=", c.Revision)).Then(ops...).Commit()
	if err != nil {
		return err
	}
	if !resp.Succeeded {
		return ErrBusy
	}
	return nil
}
func (e *Etcd) Rename(ctx context.Context, c Client, name string) error {
	c.Nickname = name
	return e.updateClient(ctx, c, clientv3.OpPut(clientKey(c.ID), encode(clientRecord{c, c.SecretHash})))
}
func (e *Etcd) Reset(ctx context.Context, c Client) error {
	return e.updateClient(ctx, c, clientv3.OpDelete(clientKey(c.ID)), clientv3.OpDelete(ticketKey(c.ID)))
}
func (e *Etcd) Enqueue(ctx context.Context, c Client) error {
	if c.GameID != "" {
		return ErrBusy
	}
	lease, err := e.client.Grant(ctx, 30)
	if err != nil {
		return err
	}
	resp, err := e.client.Txn(ctx).If(clientv3.Compare(clientv3.ModRevision(clientKey(c.ID)), "=", c.Revision), clientv3.Compare(clientv3.CreateRevision(ticketKey(c.ID)), "=", 0)).Then(clientv3.OpPut(ticketKey(c.ID), c.ID, clientv3.WithLease(lease.ID))).Commit()
	if err != nil || !resp.Succeeded {
		_, _ = e.client.Revoke(ctx, lease.ID)
		if err != nil {
			return err
		}
		return ErrBusy
	}
	return nil
}
func (e *Etcd) Cancel(ctx context.Context, c Client) error {
	return e.updateClient(ctx, c, clientv3.OpDelete(ticketKey(c.ID)))
}
func (e *Etcd) Queued(ctx context.Context, c Client) (bool, error) {
	resp, err := e.client.Get(ctx, ticketKey(c.ID))
	if err != nil {
		return false, err
	}
	if len(resp.Kvs) == 0 {
		return false, nil
	}
	_, err = e.client.KeepAliveOnce(ctx, clientv3.LeaseID(resp.Kvs[0].Lease))
	return err == nil, err
}
func (e *Etcd) Tickets(ctx context.Context) ([]Ticket, error) {
	resp, err := e.client.Get(ctx, "/moonchess/tickets/", clientv3.WithPrefix(), clientv3.WithSort(clientv3.SortByCreateRevision, clientv3.SortAscend))
	if err != nil {
		return nil, err
	}
	var out []Ticket
	for _, kv := range resp.Kvs {
		out = append(out, Ticket{string(kv.Value), kv.ModRevision, int64(kv.Lease)})
	}
	return out, nil
}

// Both client pointers, both leased tickets and room creation share one CAS.
// A crash before this transaction leaves only an unreachable checkpoint.
func (e *Etcd) Match(ctx context.Context, a, b Ticket, gameID, checkpoint string) (bool, error) {
	if a.ClientID == b.ClientID {
		return false, nil
	}
	white, err := e.readClient(ctx, a.ClientID)
	if err != nil {
		return false, err
	}
	black, err := e.readClient(ctx, b.ClientID)
	if err != nil {
		return false, err
	}
	if white.GameID != "" || black.GameID != "" {
		return false, nil
	}
	white.GameID = gameID
	black.GameID = gameID
	r := Record{GameID: gameID, HeadRef: checkpoint, CheckpointRef: checkpoint, Phase: "active", WhiteClientID: white.ID, BlackClientID: black.ID}
	resp, err := e.client.Txn(ctx).If(
		clientv3.Compare(clientv3.ModRevision(clientKey(white.ID)), "=", white.Revision), clientv3.Compare(clientv3.ModRevision(clientKey(black.ID)), "=", black.Revision),
		clientv3.Compare(clientv3.ModRevision(ticketKey(a.ClientID)), "=", a.Revision), clientv3.Compare(clientv3.ModRevision(ticketKey(b.ClientID)), "=", b.Revision),
		clientv3.Compare(clientv3.LeaseValue(ticketKey(a.ClientID)), "=", a.Lease), clientv3.Compare(clientv3.LeaseValue(ticketKey(b.ClientID)), "=", b.Lease),
		clientv3.Compare(clientv3.CreateRevision(metaKey(gameID)), "=", 0),
	).Then(clientv3.OpDelete(ticketKey(a.ClientID)), clientv3.OpDelete(ticketKey(b.ClientID)), clientv3.OpPut(clientKey(white.ID), encode(clientRecord{white, white.SecretHash})), clientv3.OpPut(clientKey(black.ID), encode(clientRecord{black, black.SecretHash})), clientv3.OpPut(metaKey(gameID), encode(r))).Commit()
	if err != nil {
		return false, err
	}
	return resp.Succeeded, nil
}
func (e *Etcd) Leave(ctx context.Context, c Client) error {
	c.GameID = ""
	return e.updateClient(ctx, c, clientv3.OpPut(clientKey(c.ID), encode(clientRecord{c, c.SecretHash})))
}
