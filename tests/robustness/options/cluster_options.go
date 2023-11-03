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
	"encoding/json"
	"fmt"
	"math/rand"
	"time"

	"go.etcd.io/etcd/server/v3/embed"
	"go.etcd.io/etcd/tests/v3/framework/e2e"
)

var Rand = rand.New(rand.NewSource(time.Now().UnixNano()))

// ClusterOptions is an array of EPClusterOptions, with functions to chain different configs together.
// Each WithXX function of ClusterOptions is able to take multiple inputs and randomly pick one value to construct a config dynamically.
// This function would make each test run get a different cluster config.
type ClusterOptions []e2e.EPClusterOption

func NewClusterOptions() *ClusterOptions {
	return &ClusterOptions{}
}

// WithClusterOptions takes an array of input *ClusterOptions, and randomly picks one value of *ClusterOptions when constructing the config.
// The picked value itself is a list of EPClusterOption.
// This function is mainly used to group strongly coupled config options together, so that we can dynamically test different groups of options.
func (opts *ClusterOptions) WithClusterOptions(input ...*ClusterOptions) *ClusterOptions {
	f := func(c *e2e.EtcdProcessClusterConfig) {
		optsPicked := input[Rand.Intn(len(input))]
		for _, opt := range *optsPicked {
			opt(c)
		}
	}
	*opts = append(*opts, f)
	return opts
}

// PrintNonDefaultServerConfig the fields in cfg that are not set by default.
func PrintNonDefaultServerConfig(cfg embed.Config) error {
	cfgDefault := *embed.NewConfig()

	s1, err1 := json.Marshal(cfg)
	s2, err2 := json.Marshal(cfgDefault)
	if err1 != nil || err2 != nil {
		return fmt.Errorf("sizhangDebug: failed to convert embed.Config to json")
	}
	// a map container to decode the JSON structure into
	c1 := make(map[string]json.RawMessage)
	c2 := make(map[string]json.RawMessage)
	// unmarschal JSON
	err1 = json.Unmarshal(s1, &c1)
	err2 = json.Unmarshal(s2, &c2)
	// panic on error
	if err1 != nil || err2 != nil {
		return fmt.Errorf("sizhangDebug: failed to parse embed.Config json")
	}
	fmt.Printf("-----------------------------\nsizhangDebug: cluster ServerConfig = {\n")
	for k, v := range c1 {
		v2, ok := c2[k]
		if ok && string(v2) != string(v) {
			fmt.Printf("sizhangDebug: %s = %s\n", k, string(v))
		}
	}
	fmt.Printf("sizhangDebug: }\n-----------------------------\n")
	return nil
}
