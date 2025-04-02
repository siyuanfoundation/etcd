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

package membership

import (
	"crypto/sha1"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.etcd.io/etcd/api/v3/membershippb"
	"go.etcd.io/etcd/client/pkg/v3/types"
)

// RaftAttributes represents the raft related attributes of an etcd member.
type RaftAttributes struct {
	// PeerURLs is the list of peers in the raft cluster.
	// TODO(philips): ensure these are URLs
	PeerURLs []string `json:"peerURLs"`
	// IsLearner indicates if the member is raft learner.
	IsLearner bool `json:"isLearner,omitempty"`
}

type Feature struct {
	Name string `json:"name,omitempty"`
	// Enabled indicates if the feature is enabled.
	Enabled bool `json:"enabled,omitempty"`
}

type ClusterParams struct {
	FeatureGates map[string]bool `json:"featureGates,omitempty"`
}

func (c *ClusterParams) String() string {
	if c == nil {
		return ""
	}
	b, err := json.Marshal(c)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func (c *ClusterParams) Clone() *ClusterParams {
	if c == nil {
		return nil
	}
	cp := &ClusterParams{}
	if c.FeatureGates != nil {
		cp.FeatureGates = make(map[string]bool)
		for k, v := range c.FeatureGates {
			cp.FeatureGates[k] = v
		}
	}
	return cp
}

func (c *ClusterParams) ToProto() *membershippb.ClusterParams {
	if c == nil {
		return nil
	}
	ret := membershippb.ClusterParams{}
	if c.FeatureGates == nil {
		return &ret
	}
	for k, v := range c.FeatureGates {
		ret.FeatureGates = append(ret.FeatureGates, &membershippb.Feature{Name: k, Enabled: v})
	}
	return &ret
}

// Attributes represents all the non-raft related attributes of an etcd member.
type Attributes struct {
	Name                  string         `json:"name,omitempty"`
	ClientURLs            []string       `json:"clientURLs,omitempty"`
	ProposedClusterParams *ClusterParams `json:"proposedClusterParams,omitempty"`
}

type Member struct {
	ID types.ID `json:"id"`
	RaftAttributes
	Attributes
}

// NewMember creates a Member without an ID and generates one based on the
// cluster name, peer URLs, and time. This is used for bootstrapping/adding new member.
func NewMember(name string, peerURLs types.URLs, clusterName string, now *time.Time) *Member {
	memberID := computeMemberID(peerURLs, clusterName, now)
	return newMember(name, peerURLs, memberID, false)
}

// NewMemberAsLearner creates a learner Member without an ID and generates one based on the
// cluster name, peer URLs, and time. This is used for adding new learner member.
func NewMemberAsLearner(name string, peerURLs types.URLs, clusterName string, now *time.Time) *Member {
	memberID := computeMemberID(peerURLs, clusterName, now)
	return newMember(name, peerURLs, memberID, true)
}

func computeMemberID(peerURLs types.URLs, clusterName string, now *time.Time) types.ID {
	peerURLstrs := peerURLs.StringSlice()
	sort.Strings(peerURLstrs)
	joinedPeerUrls := strings.Join(peerURLstrs, "")
	b := []byte(joinedPeerUrls)

	b = append(b, []byte(clusterName)...)
	if now != nil {
		b = append(b, []byte(fmt.Sprintf("%d", now.Unix()))...)
	}

	hash := sha1.Sum(b)
	return types.ID(binary.BigEndian.Uint64(hash[:8]))
}

func newMember(name string, peerURLs types.URLs, memberID types.ID, isLearner bool) *Member {
	m := &Member{
		RaftAttributes: RaftAttributes{
			PeerURLs:  peerURLs.StringSlice(),
			IsLearner: isLearner,
		},
		Attributes: Attributes{Name: name},
		ID:         memberID,
	}
	return m
}

func (m *Member) Clone() *Member {
	if m == nil {
		return nil
	}
	mm := &Member{
		ID: m.ID,
		RaftAttributes: RaftAttributes{
			IsLearner: m.IsLearner,
		},
		Attributes: Attributes{
			Name:                  m.Name,
			ProposedClusterParams: m.ProposedClusterParams.Clone(),
		},
	}
	if m.PeerURLs != nil {
		mm.PeerURLs = make([]string, len(m.PeerURLs))
		copy(mm.PeerURLs, m.PeerURLs)
	}
	if m.ClientURLs != nil {
		mm.ClientURLs = make([]string, len(m.ClientURLs))
		copy(mm.ClientURLs, m.ClientURLs)
	}
	return mm
}

func (m *Member) IsStarted() bool {
	return len(m.Name) != 0
}

// MembersByID implements sort by ID interface
type MembersByID []*Member

func (ms MembersByID) Len() int           { return len(ms) }
func (ms MembersByID) Less(i, j int) bool { return ms[i].ID < ms[j].ID }
func (ms MembersByID) Swap(i, j int)      { ms[i], ms[j] = ms[j], ms[i] }

// MembersByPeerURLs implements sort by peer urls interface
type MembersByPeerURLs []*Member

func (ms MembersByPeerURLs) Len() int { return len(ms) }
func (ms MembersByPeerURLs) Less(i, j int) bool {
	return ms[i].PeerURLs[0] < ms[j].PeerURLs[0]
}
func (ms MembersByPeerURLs) Swap(i, j int) { ms[i], ms[j] = ms[j], ms[i] }
