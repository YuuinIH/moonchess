package control

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

type Etcd struct{ client *clientv3.Client }

func OpenEtcd(endpoints []string) (*Etcd, error) {
	c, err := clientv3.New(clientv3.Config{Endpoints: endpoints, DialTimeout: 5 * time.Second})
	if err != nil {
		return nil, err
	}
	return &Etcd{client: c}, nil
}
func (e *Etcd) Close() error    { return e.client.Close() }
func metaKey(id string) string  { return path.Join("/moonchess/games", id, "meta") }
func ownerKey(id string) string { return path.Join("/moonchess/games", id, "owner") }
func progressKey(id, worker string) string {
	return path.Join("/moonchess/games", id, "progress", worker)
}
func encode(v any) string { b, _ := json.Marshal(v); return string(b) }

func (e *Etcd) Create(ctx context.Context, id, checkpoint string) (Record, error) {
	r := Record{GameID: id, HeadRef: checkpoint, CheckpointRef: checkpoint, Phase: "active"}
	resp, err := e.client.Txn(ctx).If(clientv3.Compare(clientv3.CreateRevision(metaKey(id)), "=", 0)).Then(clientv3.OpPut(metaKey(id), encode(r))).Commit()
	if err != nil {
		return Record{}, err
	}
	if !resp.Succeeded {
		return Record{}, ErrBusy
	}
	return e.Get(ctx, id)
}

func (e *Etcd) Get(ctx context.Context, id string) (Record, error) {
	resp, err := e.client.Get(ctx, metaKey(id))
	if err != nil {
		return Record{}, err
	}
	if len(resp.Kvs) == 0 {
		return Record{}, ErrNotFound
	}
	var r Record
	if err := json.Unmarshal(resp.Kvs[0].Value, &r); err != nil {
		return Record{}, err
	}
	r.Revision = resp.Kvs[0].ModRevision
	owner, err := e.client.Get(ctx, ownerKey(id))
	if err != nil {
		return Record{}, err
	}
	if len(owner.Kvs) != 0 {
		var o struct {
			Worker string `json:"worker"`
			Epoch  int64  `json:"epoch"`
		}
		if err := json.Unmarshal(owner.Kvs[0].Value, &o); err != nil {
			return Record{}, err
		}
		if o.Epoch == r.Epoch {
			r.OwnerID = o.Worker
		}
	}
	return r, nil
}
func (e *Etcd) List(ctx context.Context) ([]Record, error) {
	resp, err := e.client.Get(ctx, "/moonchess/games/", clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, kv := range resp.Kvs {
		if !strings.HasSuffix(string(kv.Key), "/meta") {
			continue
		}
		id := path.Base(path.Dir(string(kv.Key)))
		r, err := e.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}
func ownerValue(l Lease) string {
	return encode(struct {
		Worker string `json:"worker"`
		Epoch  int64  `json:"epoch"`
	}{l.WorkerID, l.Epoch})
}
func (e *Etcd) Acquire(ctx context.Context, observed Record, worker string, ttl int64) (Lease, error) {
	if observed.TargetID != "" && observed.TargetID != worker && time.Now().UnixMilli() < observed.TargetUntilUnixMS {
		return Lease{}, ErrBusy
	}
	if ttl < 1 {
		ttl = 1
	}
	grant, err := e.client.Grant(ctx, ttl)
	if err != nil {
		return Lease{}, err
	}
	l := Lease{GameID: observed.GameID, WorkerID: worker, Epoch: observed.Epoch + 1, ID: int64(grant.ID)}
	next := observed
	next.Epoch = l.Epoch
	next.OwnerID = ""
	next.TargetID = ""
	next.TargetUntilUnixMS = 0
	next.Phase = "active"
	next.Revision = 0
	event, eventErr := ownershipOp("acquired", Record{GameID: l.GameID, OwnerID: worker, Epoch: l.Epoch, CommittedSeq: next.CommittedSeq, Phase: next.Phase})
	if eventErr != nil {
		_, _ = e.client.Revoke(ctx, grant.ID)
		return Lease{}, eventErr
	}
	resp, err := e.client.Txn(ctx).If(clientv3.Compare(clientv3.CreateRevision(ownerKey(l.GameID)), "=", 0), clientv3.Compare(clientv3.ModRevision(metaKey(l.GameID)), "=", observed.Revision)).Then(clientv3.OpPut(ownerKey(l.GameID), ownerValue(l), clientv3.WithLease(grant.ID)), clientv3.OpPut(metaKey(l.GameID), encode(next)), event).Commit()
	if err != nil || !resp.Succeeded {
		_, _ = e.client.Revoke(context.Background(), grant.ID)
		if err != nil {
			return Lease{}, err
		}
		return Lease{}, ErrBusy
	}
	return l, nil
}
func (e *Etcd) Renew(ctx context.Context, l Lease) error {
	resp, err := e.client.Get(ctx, ownerKey(l.GameID))
	if err != nil {
		return err
	}
	if len(resp.Kvs) == 0 || string(resp.Kvs[0].Value) != ownerValue(l) || int64(resp.Kvs[0].Lease) != l.ID {
		return ErrFenced
	}
	_, err = e.client.KeepAliveOnce(ctx, clientv3.LeaseID(l.ID))
	return err
}
func (e *Etcd) update(ctx context.Context, l Lease, observed Record, next Record) (Record, error) {
	next.OwnerID = ""
	next.Revision = 0
	ops := []clientv3.Op{clientv3.OpPut(metaKey(l.GameID), encode(next))}
	if next.Phase != observed.Phase {
		eventRecord := next
		eventRecord.OwnerID = l.WorkerID
		event, err := ownershipOp(next.Phase, eventRecord)
		if err != nil {
			return Record{}, err
		}
		ops = append(ops, event)
	}
	resp, err := e.client.Txn(ctx).If(clientv3.Compare(clientv3.Value(ownerKey(l.GameID)), "=", ownerValue(l)), clientv3.Compare(clientv3.LeaseValue(ownerKey(l.GameID)), "=", l.ID), clientv3.Compare(clientv3.ModRevision(metaKey(l.GameID)), "=", observed.Revision)).Then(ops...).Commit()
	if err != nil {
		return Record{}, err
	}
	if !resp.Succeeded {
		return Record{}, ErrFenced
	}
	return e.Get(ctx, l.GameID)
}
func (e *Etcd) Commit(ctx context.Context, l Lease, observed Record, head string) (Record, error) {
	if observed.OwnerID != l.WorkerID || observed.Epoch != l.Epoch || observed.Phase != "active" {
		return Record{}, ErrFenced
	}
	next := observed
	next.CommittedSeq++
	next.HeadRef = head
	return e.update(ctx, l, observed, next)
}
func (e *Etcd) Checkpoint(ctx context.Context, l Lease, observed Record, ref string) (Record, error) {
	if observed.OwnerID != l.WorkerID || observed.Epoch != l.Epoch {
		return Record{}, ErrFenced
	}
	next := observed
	next.FallbackRef = observed.CheckpointRef
	next.FallbackSeq = observed.CheckpointSeq
	next.CheckpointRef = ref
	next.CheckpointSeq = observed.CommittedSeq
	return e.update(ctx, l, observed, next)
}
func (e *Etcd) SetProgress(ctx context.Context, id, worker string, p Progress) error {
	_, err := e.client.Put(ctx, progressKey(id, worker), encode(p))
	return err
}
func (e *Etcd) GetProgress(ctx context.Context, id, worker string) (Progress, error) {
	resp, err := e.client.Get(ctx, progressKey(id, worker))
	if err != nil {
		return Progress{}, err
	}
	if len(resp.Kvs) == 0 {
		return Progress{}, ErrNotFound
	}
	var p Progress
	err = json.Unmarshal(resp.Kvs[0].Value, &p)
	return p, err
}
func (e *Etcd) BeginMigration(ctx context.Context, l Lease, observed Record, target string) (Record, error) {
	if target == l.WorkerID || target == "" {
		return Record{}, errors.New("invalid migration target")
	}
	next := observed
	next.TargetID = target
	next.TargetUntilUnixMS = time.Now().Add(10 * time.Second).UnixMilli()
	next.Phase = "migrating"
	return e.update(ctx, l, observed, next)
}
func (e *Etcd) CancelMigration(ctx context.Context, l Lease, observed Record) (Record, error) {
	if observed.Phase != "migrating" || observed.OwnerID != l.WorkerID || observed.Epoch != l.Epoch {
		return Record{}, ErrFenced
	}
	next := observed
	next.Phase = "active"
	next.TargetID = ""
	next.TargetUntilUnixMS = 0
	return e.update(ctx, l, observed, next)
}
func (e *Etcd) Release(ctx context.Context, l Lease) error {
	event, eventErr := ownershipOp("released", Record{GameID: l.GameID, Epoch: l.Epoch, Phase: "between owners"})
	if eventErr != nil {
		return eventErr
	}
	resp, err := e.client.Txn(ctx).If(clientv3.Compare(clientv3.Value(ownerKey(l.GameID)), "=", ownerValue(l)), clientv3.Compare(clientv3.LeaseValue(ownerKey(l.GameID)), "=", l.ID)).Then(clientv3.OpDelete(ownerKey(l.GameID)), event).Commit()
	if err != nil {
		return err
	}
	if !resp.Succeeded {
		return ErrFenced
	}
	// The owner key is already gone. Revoke is best effort cleanup of its lease.
	_, _ = e.client.Revoke(ctx, clientv3.LeaseID(l.ID))
	return nil
}
