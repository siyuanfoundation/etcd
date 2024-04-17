// Copyright 2024 The etcd Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/pkg/v3/stringutil"
	"go.etcd.io/etcd/tests/v3/framework/e2e"
)

func TestReproduce17780(t *testing.T) {
	e2e.BeforeTest(t)

	ctx := context.TODO()
	clus, cerr := e2e.NewEtcdProcessCluster(ctx, t,
		e2e.WithClusterSize(1),
		e2e.WithGoFailEnabled(true),
		e2e.WithSnapshotCount(1000),
		e2e.WithCompactionBatchLimit(100),
		e2e.WithWatchProcessNotifyInterval(100*time.Millisecond),
	)
	require.NoError(t, cerr)

	t.Cleanup(func() { clus.Stop() })

	cli := newClient(t, clus.EndpointsGRPC(), e2e.ClientConfig{})

	// Revision: 2 -> 198 for new keys
	n := 198
	valueSize := 16
	for i := 2; i <= n; i++ {
		_, err := cli.Put(ctx, fmt.Sprintf("%d", i), stringutil.RandString(uint(valueSize)))
		require.NoError(t, err)
	}

	// Revision: 199 -> 201 for delete keys with compared revision
	for i := 199; i <= 201; i++ {
		key := fmt.Sprintf("%d", i-100)
		rev := i - 100

		resp, err := cli.Txn(ctx).If(clientv3.Cmp{
			Result:      pb.Compare_EQUAL,
			Target:      pb.Compare_MOD,
			Key:         []byte(key),
			TargetUnion: &pb.Compare_ModRevision{int64(rev)},
		}).Then(clientv3.OpDelete(key)).
			Else(clientv3.OpGet("/")).Commit()
		require.NoError(t, err)
		require.True(t, resp.Succeeded)
	}

	require.NoError(t, clus.Procs[0].Failpoints().SetupHTTP(ctx, "compactBeforeSetFinishedCompact", `panic`))

	_, err := cli.Compact(ctx, 201, clientv3.WithCompactPhysical())
	require.Error(t, err)

	require.NoError(t, clus.Restart(ctx))

	resp, err := cli.Get(ctx, fmt.Sprintf("%d", 3))
	require.NoError(t, err)
	require.True(t, resp.Count == 1)

	resp, err = cli.Get(ctx, fmt.Sprintf("%d", 99))
	require.NoError(t, err)
	require.True(t, resp.Count == 0)
	// The 199/200/201 revision has been deleted. The max rev is 198.
	//
	// That's why revision has been decreased.
	require.GreaterOrEqual(t, resp.Header.Revision, int64(201))
}
