// Copyright 2015 The etcd Authors
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

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"go.etcd.io/etcd/server/v3/etcdserver"
	"go.etcd.io/etcd/server/v3/storage/backend"
	"go.etcd.io/etcd/server/v3/storage/schema"
	"go.etcd.io/etcd/tests/v3/framework/integration"
)

func TestPauseMember(t *testing.T) {
	integration.BeforeTest(t)

	c := integration.NewCluster(t, &integration.ClusterConfig{Size: 5})
	defer c.Terminate(t)

	for i := 0; i < 5; i++ {
		c.Members[i].Pause()
		membs := append([]*integration.Member{}, c.Members[:i]...)
		membs = append(membs, c.Members[i+1:]...)
		c.WaitMembersForLeader(t, membs)
		clusterMustProgress(t, membs)
		c.Members[i].Resume()
	}
	c.WaitMembersForLeader(t, c.Members)
	clusterMustProgress(t, c.Members)
}

func TestRestartMember(t *testing.T) {
	integration.BeforeTest(t)
	c := integration.NewCluster(t, &integration.ClusterConfig{Size: 3, UseBridge: true})
	defer c.Terminate(t)

	for i := 0; i < 3; i++ {
		c.Members[i].Stop(t)
		membs := append([]*integration.Member{}, c.Members[:i]...)
		membs = append(membs, c.Members[i+1:]...)
		c.WaitMembersForLeader(t, membs)
		clusterMustProgress(t, membs)
		err := c.Members[i].Restart(t)
		if err != nil {
			t.Fatal(err)
		}
	}
	c.WaitMembersForLeader(t, c.Members)
	clusterMustProgress(t, c.Members)
}

func TestLaunchDuplicateMemberShouldFail(t *testing.T) {
	integration.BeforeTest(t)
	size := 3
	c := integration.NewCluster(t, &integration.ClusterConfig{Size: size})
	m := c.Members[0].Clone(t)
	m.DataDir = t.TempDir()
	defer c.Terminate(t)

	if err := m.Launch(); err == nil {
		t.Errorf("unexpect successful launch")
	} else {
		t.Logf("launch failed as expected: %v", err)
		assert.Contains(t, err.Error(), "has already been bootstrapped")
	}
}

func TestSnapshotAndRestartMember(t *testing.T) {
	integration.BeforeTest(t)
	m := integration.MustNewMember(t, integration.MemberConfig{Name: "snapAndRestartTest", UseBridge: true})
	m.SnapshotCount = 100
	m.Launch()
	defer m.Terminate(t)
	defer m.Client.Close()
	m.WaitOK(t)

	var err error
	for i := 0; i < 120; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), integration.RequestTimeout)
		key := fmt.Sprintf("foo%d", i)
		_, err = m.Client.Put(ctx, "/"+key, "bar")
		if err != nil {
			t.Fatalf("#%d: create on %s error: %v", i, m.URL(), err)
		}
		cancel()
	}
	m.Stop(t)
	m.Restart(t)

	m.WaitOK(t)
	for i := 0; i < 120; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), integration.RequestTimeout)
		key := fmt.Sprintf("foo%d", i)
		resp, err := m.Client.Get(ctx, "/"+key)
		if err != nil {
			t.Fatalf("#%d: get on %s error: %v", i, m.URL(), err)
		}
		cancel()

		if len(resp.Kvs) != 1 || string(resp.Kvs[0].Value) != "bar" {
			t.Errorf("#%d: got = %v, want %v", i, resp.Kvs[0], "bar")
		}
	}
}

func TestRemoveMember(t *testing.T) {
	integration.BeforeTest(t)
	c := integration.NewCluster(t, &integration.ClusterConfig{Size: 3, UseBridge: true, BackendBatchInterval: 1000 * time.Second})
	defer c.Terminate(t)
	c.WaitLeader(t)
	time.Sleep(etcdserver.HealthInterval)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	_, err := c.Members[2].Client.MemberRemove(ctx, uint64(c.Members[0].Server.MemberId()))
	cancel()

	if err != nil {
		t.Fatalf("should accept removing member: %s", err)
	}
	// fmt.Printf("sizhangDebug: removed member m0, memberCount(m1) = %d, memberCount(m2) = %d\n", mustReadMemberCountFromBackend(t, c.Members[1].Server.Backend()), mustReadMemberCountFromBackend(t, c.Members[2].Server.Backend()))
	c.Members[0].Stop(t)
	time.Sleep(etcdserver.HealthInterval)

	// check member count in m1 is correct.
	memberCount := mustReadMemberCountFromBackend(t, c.Members[1].Server.Backend())
	if memberCount != 3 {
		t.Errorf("Expect MemberCountFromBackend=3 after deleting 1 member, got %d", memberCount)
	}
	membersResp, err := c.Members[1].Client.MemberList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(membersResp.Members) != 2 {
		t.Errorf("Expect len(MemberList)=2 after deleting 1 members, got %d", len(membersResp.Members))
	}
	// check member count in m2 is correct.
	memberCount = mustReadMemberCountFromBackend(t, c.Members[2].Server.Backend())
	if memberCount != 3 {
		t.Errorf("Expect MemberCountFromBackend=3 after deleting 1 member, got %d", memberCount)
	}
	membersResp, err = c.Members[2].Client.MemberList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(membersResp.Members) != 2 {
		t.Errorf("Expect len(MemberList)=2 after deleting 1 members, got %d", len(membersResp.Members))
	}
}

func mustReadMemberCountFromBackend(t *testing.T, be backend.Backend) int {
	tx := be.ReadTx()
	tx.RLock()
	defer tx.RUnlock()
	count := 0
	err := tx.UnsafeForEach(schema.Members, func(k, v []byte) error {
		count++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return count
}
