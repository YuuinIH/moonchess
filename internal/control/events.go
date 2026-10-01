package control

import (
	"context"
	"encoding/json"

	"github.com/yuuinih/moonchess/internal/id"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// Ownership events are small control metadata, committed with the transition.
// Their etcd revision is a cross-gateway resume cursor, independent of moves.
type OwnershipEvent struct {
	Kind     string `json:"kind"`
	Control  Record `json:"control"`
	Revision int64  `json:"revision"`
}
type Timeline interface {
	OwnershipEvents(context.Context, string, int64) ([]OwnershipEvent, error)
}

func ownershipOp(kind string, r Record) (clientv3.Op, error) {
	suffix, err := id.RandomHex(16)
	if err != nil {
		return clientv3.Op{}, err
	}
	return clientv3.OpPut("/moonchess/events/"+r.GameID+"/"+suffix, encode(OwnershipEvent{Kind: kind, Control: r})), nil
}
func (e *Etcd) OwnershipEvents(ctx context.Context, gameID string, after int64) ([]OwnershipEvent, error) {
	resp, err := e.client.Get(ctx, "/moonchess/events/"+gameID+"/", clientv3.WithPrefix(), clientv3.WithMinModRev(after+1), clientv3.WithSort(clientv3.SortByModRevision, clientv3.SortAscend))
	if err != nil {
		return nil, err
	}
	var out []OwnershipEvent
	for _, kv := range resp.Kvs {
		var v OwnershipEvent
		if err = json.Unmarshal(kv.Value, &v); err != nil {
			return nil, err
		}
		v.Revision = kv.ModRevision
		out = append(out, v)
	}
	return out, nil
}
