// Copyright 2017 The etcd Authors
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

package etcdhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/client/pkg/v3/types"
	"go.etcd.io/etcd/server/v3/auth"
	"go.etcd.io/etcd/server/v3/config"
	"go.etcd.io/raft/v3"
)

const (
	PathHealth      = "/health"
	PathProxyHealth = "/proxy/health"
)

type ServerHealth interface {
	Alarms() []*pb.AlarmMember
	Leader() types.ID
	Range(context.Context, *pb.RangeRequest) (*pb.RangeResponse, error)
	Config() config.ServerConfig
	AuthStore() auth.AuthStore
}

// HandleHealth registers metrics and health handlers. it checks health by using v3 range request
// and its corresponding timeout.
func HandleHealth(lg *zap.Logger, mux *http.ServeMux, srv ServerHealth) {
	mux.Handle(PathHealth, NewHealthHandler(lg, func(excludedAlarms StringSet, serializable bool) Health {
		if h := checkAlarms(lg, srv, excludedAlarms); h.Health != "true" {
			return h
		}
		if h := checkLeader(lg, srv, serializable); h.Health != "true" {
			return h
		}
		return checkAPI(lg, srv, serializable)
	}))

	installLivezEndpoints(lg, mux, srv)
	installReayzEndpoints(lg, mux, srv)
}

// NewHealthHandler handles '/health' requests.
func NewHealthHandler(lg *zap.Logger, hfunc func(excludedAlarms StringSet, Serializable bool) Health) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			lg.Warn("/health error", zap.Int("status-code", http.StatusMethodNotAllowed))
			return
		}
		excludedAlarms := getQuerySet(r, "exclude")
		// Passing the query parameter "serializable=true" ensures that the
		// health of the local etcd is checked vs the health of the cluster.
		// This is useful for probes attempting to validate the liveness of
		// the etcd process vs readiness of the cluster to serve requests.
		serializableFlag := getSerializableFlag(r)
		h := hfunc(excludedAlarms, serializableFlag)
		defer func() {
			if h.Health == "true" {
				healthSuccess.Inc()
			} else {
				healthFailed.Inc()
			}
		}()
		d, _ := json.Marshal(h)
		if h.Health != "true" {
			http.Error(w, string(d), http.StatusServiceUnavailable)
			lg.Warn("/health error", zap.String("output", string(d)), zap.Int("status-code", http.StatusServiceUnavailable))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write(d)
		lg.Debug("/health OK", zap.Int("status-code", http.StatusOK))
	}
}

// newHealthHandler generates a http HandlerFunc for a health check function hfunc.
func newHealthHandler(path string, lg *zap.Logger, hfunc func(*http.Request) (Health, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			lg.Warn("/health error", zap.Int("status-code", http.StatusMethodNotAllowed))
			return
		}
		h, err := hfunc(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		d, _ := json.Marshal(h)
		if h.Health != "true" {
			http.Error(w, string(d), http.StatusServiceUnavailable)
			lg.Warn("Health check error", zap.String("path", path), zap.String("output", string(d)), zap.Int("status-code", http.StatusServiceUnavailable))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write(d)
		lg.Debug("Health check OK", zap.String("path", path), zap.String("output", string(d)), zap.Int("status-code", http.StatusOK))
	}
}

var (
	healthSuccess = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "etcd",
		Subsystem: "server",
		Name:      "health_success",
		Help:      "The total number of successful health checks",
	})
	healthFailed = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "etcd",
		Subsystem: "server",
		Name:      "health_failures",
		Help:      "The total number of failed health checks",
	})
)

func init() {
	prometheus.MustRegister(healthSuccess)
	prometheus.MustRegister(healthFailed)
}

// Health defines etcd server health status.
// TODO: remove manual parsing in etcdctl cluster-health
type Health struct {
	Health string `json:"health"`
	Reason string `json:"reason"`
}

func getQuerySet(r *http.Request, query string) StringSet {
	querySet := make(map[string]struct{})
	qs, found := r.URL.Query()[query]
	if found {
		for _, q := range qs {
			if len(q) == 0 {
				continue
			}
			querySet[q] = struct{}{}
		}
	}
	return querySet
}

func getSerializableFlag(r *http.Request) bool {
	return r.URL.Query().Get("serializable") == "true"
}

// TODO: etcdserver.ErrNoLeader in health API

func checkAlarms(lg *zap.Logger, srv ServerHealth, excludedAlarms StringSet) Health {
	h := Health{Health: "true"}
	as := srv.Alarms()
	if len(as) > 0 {
		for _, v := range as {
			alarmName := v.Alarm.String()
			if _, found := excludedAlarms[alarmName]; found {
				lg.Debug("/health excluded alarm", zap.String("alarm", v.String()))
				continue
			}

			h.Health = "false"
			switch v.Alarm {
			case pb.AlarmType_NOSPACE:
				h.Reason = "ALARM NOSPACE"
			case pb.AlarmType_CORRUPT:
				h.Reason = "ALARM CORRUPT"
			default:
				h.Reason = "ALARM UNKNOWN"
			}
			lg.Warn("serving /health false due to an alarm", zap.String("alarm", v.String()))
			return h
		}
	}

	return h
}

func checkLeader(lg *zap.Logger, srv ServerHealth, serializable bool) Health {
	h := Health{Health: "true"}
	if !serializable && (uint64(srv.Leader()) == raft.None) {
		h.Health = "false"
		h.Reason = "RAFT NO LEADER"
		lg.Warn("serving /health false; no leader")
	}
	return h
}

func checkAPI(lg *zap.Logger, srv ServerHealth, serializable bool) Health {
	h := Health{Health: "true"}
	cfg := srv.Config()
	ctx, cancel := context.WithTimeout(context.Background(), cfg.ReqTimeout())
	_, err := srv.Range(ctx, &pb.RangeRequest{KeysOnly: true, Limit: 1, Serializable: serializable})
	cancel()
	if err != nil && err != auth.ErrUserEmpty && err != auth.ErrPermissionDenied {
		h.Health = "false"
		h.Reason = fmt.Sprintf("RANGE ERROR:%s", err)
		lg.Warn("serving /health false; Range fails", zap.Error(err))
		return h
	}
	lg.Debug("serving /health true")
	return h
}

type HealthCheck func(ctx context.Context) error

type CheckRegistry struct {
	path string

	checks map[string]HealthCheck
	// the list of checkNames are needed to ensure the order of checks.
	checkNames []string
}

func installLivezEndpoints(lg *zap.Logger, mux *http.ServeMux, server ServerHealth) {
	reg := CheckRegistry{path: "/livez", checks: make(map[string]HealthCheck)}
	reg.Register("serializable_read", serializableReadCheck(server))
	reg.InstallHttpEndpoints(lg, mux)
}

func installReayzEndpoints(lg *zap.Logger, mux *http.ServeMux, server ServerHealth) {
	reg := CheckRegistry{path: "/readyz", checks: make(map[string]HealthCheck)}
	reg.Register("data_corruption", activeAlarmCheck(server, pb.AlarmType_CORRUPT))
	reg.Register("serializable_read", serializableReadCheck(server))
	reg.InstallHttpEndpoints(lg, mux)
}

func (reg *CheckRegistry) Register(name string, check HealthCheck) {
	reg.checkNames = append(reg.checkNames, name)
	reg.checks[name] = check
}

func (reg *CheckRegistry) InstallHttpEndpoints(lg *zap.Logger, mux *http.ServeMux) {
	// installs the http handler for the root path.
	reg.installHttpEndpoint(lg, mux, reg.path, reg.checkNames...)
	for _, checkName := range reg.checkNames {
		// installs the http handler for the individual check sub path.
		reg.installHttpEndpoint(lg, mux, path.Join(reg.path, checkName), checkName)
	}
}

func (reg *CheckRegistry) runHealthChecks(ctx context.Context, checkNames ...string) (Health, error) {
	h := Health{Health: "true"}
	failedChecks := []string{}
	var individualCheckOutput bytes.Buffer
	for _, checkName := range checkNames {
		check, found := reg.checks[checkName]
		if !found {
			return Health{Health: "false"}, fmt.Errorf("Health check: %s not registered", checkName)
		}
		if err := check(ctx); err != nil {
			fmt.Fprintf(&individualCheckOutput, "[-]%s failed: %v\n", checkName, err)
			failedChecks = append(failedChecks, checkName)
		} else {
			fmt.Fprintf(&individualCheckOutput, "[+]%s ok\n", checkName)
		}
	}
	h.Reason = individualCheckOutput.String()
	if len(failedChecks) > 0 {
		h.Health = "false"
	}
	return h, nil
}

// installHttpEndpoint installs the http handler for the root path.
func (reg *CheckRegistry) installHttpEndpoint(lg *zap.Logger, mux *http.ServeMux, path string, checks ...string) {
	hfunc := func(r *http.Request) (Health, error) {
		// extracts the health check names to be excludeList from the query param
		excluded := getQuerySet(r, "exclude")
		// extracts the health check names to be allowList from the query param
		included := getQuerySet(r, "allowlist")

		filteredCheckNames, err := filterCheckList(checks, included, excluded)
		if err != nil {
			return Health{Health: "false"}, err
		}
		return reg.runHealthChecks(r.Context(), filteredCheckNames...)
	}
	mux.Handle(path, newHealthHandler(path, lg, hfunc))
}

func filterCheckList(checks []string, included StringSet, excluded StringSet) (filteredList []string, err error) {
	switch {
	case len(excluded) > 0 && len(included) > 0:
		return nil, fmt.Errorf("both an allowlist and an exclude list are specified in the query")
	case len(excluded) > 0 && len(included) == 0:
		return filterExcludedChecks(checks, excluded)
	case len(excluded) == 0 && len(included) > 0:
		return filterIncludedChecks(checks, included)
	default:
		return checks, nil
	}
}

func filterExcludedChecks(checks []string, excluded StringSet) (filteredList []string, err error) {
	for _, chk := range checks {
		if _, found := excluded[chk]; found {
			delete(excluded, chk)
			continue
		}
		filteredList = append(filteredList, chk)
	}
	if len(excluded) > 0 {
		return nil, fmt.Errorf("some health checks cannot be excluded: no matches for %s", formatQuoted(excluded.List()...))
	}
	return filteredList, nil
}

func filterIncludedChecks(checks []string, included StringSet) (filteredList []string, err error) {
	for _, chk := range checks {
		if _, found := included[chk]; found {
			delete(included, chk)
			filteredList = append(filteredList, chk)
		}
	}
	if len(included) > 0 {
		return nil, fmt.Errorf("some health checks cannot be included: no matches for %s", formatQuoted(included.List()...))
	}
	return filteredList, nil
}

// formatQuoted returns a formatted string of the health check names,
// preserving the order passed in.
func formatQuoted(names ...string) string {
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, fmt.Sprintf("%q", name))
	}
	return strings.Join(quoted, ",")
}

type StringSet map[string]struct{}

func (s StringSet) List() []string {
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	return keys
}

// activeAlarmCheck checks if a specific alarm type is active in the server.
func activeAlarmCheck(srv ServerHealth, at pb.AlarmType) func(context.Context) error {
	return func(ctx context.Context) error {
		as := srv.Alarms()
		for _, v := range as {
			if v.Alarm == at {
				return fmt.Errorf("Alarm active: %s", at.String())
			}
		}
		return nil
	}
}

func serializableReadCheck(srv ServerHealth) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		ctx = srv.AuthStore().WithRoot(ctx)
		_, err := srv.Range(ctx, &pb.RangeRequest{Key: []byte("\x00"), RangeEnd: []byte("\x00"), KeysOnly: true, Limit: 1, Serializable: true})
		if err != nil {
			return fmt.Errorf("RANGE ERROR: %s", err)
		}
		return nil
	}
}
