// Copyright 2023 The etcd Authors
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

package options

import (
	"math/rand"
	"testing"
	"time"

	"go.etcd.io/etcd/server/v3/embed"
	"go.etcd.io/etcd/tests/v3/framework/e2e"
)

func init() {
	Rand = rand.New(rand.NewSource(1))
}

func TestRandomClusterOptions(t *testing.T) {
	// fixed options first
	opts := ClusterOptions{
		e2e.WithWatchProcessNotifyInterval(100 * time.Millisecond),
	}
	// add randomizable options
	opts.WithSnapshotCount(100, 150, 200).WithClusterOptions(
		// strongly coupled ExperimentalCompactHashCheckEnabled and ExperimentalCompactHashCheckTime
		NewClusterOptions().WithExperimentalCompactHashCheckEnabled(false),
		NewClusterOptions().WithExperimentalCompactHashCheckEnabled(true).WithExperimentalCompactHashCheckTime(5*time.Second, 10*time.Second, 20*time.Second, 2*time.Minute),
	)

	expectedServerConfigs := []embed.Config{
		embed.Config{SnapshotCount: 200, ExperimentalCompactHashCheckEnabled: true, ExperimentalCompactHashCheckTime: 2 * time.Minute, ExperimentalWatchProgressNotifyInterval: 100 * time.Millisecond},
		embed.Config{SnapshotCount: 150, ExperimentalCompactHashCheckEnabled: false, ExperimentalCompactHashCheckTime: time.Minute, ExperimentalWatchProgressNotifyInterval: 100 * time.Millisecond},
		embed.Config{SnapshotCount: 200, ExperimentalCompactHashCheckEnabled: false, ExperimentalCompactHashCheckTime: time.Minute, ExperimentalWatchProgressNotifyInterval: 100 * time.Millisecond},
		embed.Config{SnapshotCount: 200, ExperimentalCompactHashCheckEnabled: true, ExperimentalCompactHashCheckTime: 10 * time.Second, ExperimentalWatchProgressNotifyInterval: 100 * time.Millisecond},
		embed.Config{SnapshotCount: 150, ExperimentalCompactHashCheckEnabled: false, ExperimentalCompactHashCheckTime: time.Minute, ExperimentalWatchProgressNotifyInterval: 100 * time.Millisecond},
		embed.Config{SnapshotCount: 200, ExperimentalCompactHashCheckEnabled: true, ExperimentalCompactHashCheckTime: 2 * time.Minute, ExperimentalWatchProgressNotifyInterval: 100 * time.Millisecond},
	}
	for i, tt := range expectedServerConfigs {
		cluster := *e2e.NewConfig(opts...)
		if cluster.ServerConfig.SnapshotCount != tt.SnapshotCount {
			t.Errorf("Test case %d: SnapshotCount = %v, want %v\n", i, cluster.ServerConfig.SnapshotCount, tt.SnapshotCount)
		}
		if cluster.ServerConfig.ExperimentalCompactHashCheckEnabled != tt.ExperimentalCompactHashCheckEnabled {
			t.Errorf("Test case %d: ExperimentalCompactHashCheckEnabled = %v, want %v\n", i, cluster.ServerConfig.ExperimentalCompactHashCheckEnabled, tt.ExperimentalCompactHashCheckEnabled)
		}
		if cluster.ServerConfig.ExperimentalCompactHashCheckTime != tt.ExperimentalCompactHashCheckTime {
			t.Errorf("Test case %d: ExperimentalCompactHashCheckTime = %v, want %v\n", i, cluster.ServerConfig.ExperimentalCompactHashCheckTime, tt.ExperimentalCompactHashCheckTime)
		}
		if cluster.ServerConfig.ExperimentalWatchProgressNotifyInterval != tt.ExperimentalWatchProgressNotifyInterval {
			t.Errorf("Test case %d: ExperimentalWatchProgressNotifyInterval = %v, want %v\n", i, cluster.ServerConfig.ExperimentalWatchProgressNotifyInterval, tt.ExperimentalWatchProgressNotifyInterval)
		}
	}

}
