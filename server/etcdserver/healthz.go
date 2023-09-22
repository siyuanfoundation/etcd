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

package etcdserver

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/server/v3/auth"
	"go.uber.org/zap"
)

type EtcdServerHealth interface {
	Alarms() []*etcdserverpb.AlarmMember
	Range(context.Context, *etcdserverpb.RangeRequest) (*etcdserverpb.RangeResponse, error)
}

// HealthChecker is a named healthz checker.
type HealthChecker interface {
	Name() string
	Check(req *http.Request) error
}

func (s *EtcdServer) InstallLivezReadyz(lg *zap.Logger, mux mux) {
	s.healthHandler.installLivez(lg, mux)
	s.healthHandler.installReadyz(lg, mux)
	s.healthHandler.installHealthz(lg, mux)
}

func (s *EtcdServer) AddHealthCheck(check HealthChecker, isLivez bool, isReadyz bool) error {
	return s.healthHandler.addHealthCheck(check, isLivez, isReadyz)
}

type HealthHandler struct {
	server EtcdServerHealth
	// lock for health check related functions.
	healthMux sync.Mutex
	// stores all the added health checks, map of HealthChecker.Name() : HealthChecker
	healthCheckStore map[string]HealthChecker
	// default checks for healthz endpoint, which is all the keys in healthCheckStore.
	healthzChecks          []string
	healthzChecksInstalled bool

	// default checks for livez endpoint
	livezChecks          []string
	livezChecksInstalled bool
	// default checks for readyz endpoint
	readyzChecks          []string
	readyzChecksInstalled bool
}

func NewHealthHandler(s EtcdServerHealth) (handler *HealthHandler, err error) {
	handler = &HealthHandler{
		server:           s,
		healthCheckStore: make(map[string]HealthChecker),
		healthzChecks:    []string{},
		livezChecks:      []string{},
		readyzChecks:     []string{},
	}
	if err = handler.addDefaultHealthChecks(); err != nil {
		return nil, err
	}
	return handler, nil
}

type stringSet map[string]struct{}

func (s stringSet) List() []string {
	keys := make([]string, len(s))

	i := 0
	for k := range s {
		keys[i] = k
		i++
	}
	return keys
}

// PingHealthz returns true automatically when checked
var PingHealthz HealthChecker = ping{}

// ping implements the simplest possible healthz checker.
type ping struct{}

func (ping) Name() string {
	return "ping"
}

// PingHealthz is a health check that returns true.
func (ping) Check(_ *http.Request) error {
	return nil
}

// healthzCheck implements HealthChecker on an arbitrary name and check function.
type healthzCheck struct {
	name  string
	check func(r *http.Request) error
}

var _ HealthChecker = &healthzCheck{}

func (c *healthzCheck) Name() string {
	return c.name
}

func (c *healthzCheck) Check(r *http.Request) error {
	return c.check(r)
}

// NamedCheck returns a healthz checker for the given name and function.
func NamedCheck(name string, check func(r *http.Request) error) HealthChecker {
	return &healthzCheck{name, check}
}

func checkAlarm(srv EtcdServerHealth, at etcdserverpb.AlarmType) error {
	as := srv.Alarms()
	if len(as) > 0 {
		for _, v := range as {
			if v.Alarm == at {
				return fmt.Errorf("Alarm active:%s", at.String())
			}
		}
	}
	return nil
}

func (h *HealthHandler) addDefaultHealthChecks() error {
	// Checks that should be included both in livez and readyz.
	h.addHealthCheck(PingHealthz, true, true)
	serializableReadCheck := NamedCheck("serializable_read", func(r *http.Request) error {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := h.server.Range(ctx, &etcdserverpb.RangeRequest{KeysOnly: true, Limit: 1, Serializable: true})
		cancel()
		if err != nil && err != auth.ErrUserEmpty && err != auth.ErrPermissionDenied {
			return fmt.Errorf("RANGE ERROR:%s", err)
		}
		return nil
	})
	h.addHealthCheck(serializableReadCheck, true, true)
	// Checks that should be included only in livez.
	// Checks that should be included only in readyz.
	corruptionAlarmCheck := NamedCheck("data_corruption", func(r *http.Request) error {
		return checkAlarm(h.server, etcdserverpb.AlarmType_CORRUPT)
	})
	h.addHealthCheck(corruptionAlarmCheck, false, true)
	return nil
}

// addHealthCheck allows you to add a HealthCheck to livez or readyz or both.
func (h *HealthHandler) addHealthCheck(check HealthChecker, isLivez bool, isReadyz bool) error {
	h.healthMux.Lock()
	defer h.healthMux.Unlock()
	if _, found := h.healthCheckStore[check.Name()]; found {
		return fmt.Errorf("Health check with the name of %s already exists.", check.Name())
	}
	// New health checks can only be added before the healthz endpoint is created.
	if h.healthzChecksInstalled {
		return fmt.Errorf("unable to add because the healthz endpoint has already been created")
	}
	if isLivez {
		if h.livezChecksInstalled {
			return fmt.Errorf("unable to add because the livez endpoint has already been created")
		}
		if isReadyz && h.readyzChecksInstalled {
			return fmt.Errorf("unable to add because the readyz endpoint has already been created")
		}
		h.livezChecks = append(h.livezChecks, check.Name())
	}
	if isReadyz {
		if h.readyzChecksInstalled {
			return fmt.Errorf("unable to add because the readyz endpoint has already been created")
		}
		h.readyzChecks = append(h.readyzChecks, check.Name())
	}
	h.healthCheckStore[check.Name()] = check
	h.healthzChecks = append(h.healthzChecks, check.Name())
	return nil
}

func (h *HealthHandler) getHealthChecksByNames(names []string) []HealthChecker {
	checks := make([]HealthChecker, len(names))
	i := 0
	for _, name := range names {
		if chk, found := h.healthCheckStore[name]; found {
			checks[i] = chk
			i++
		}
	}
	return checks[:i]
}

// installHealthz creates the healthz endpoint for this server.
func (h *HealthHandler) installHealthz(lg *zap.Logger, mux mux) {
	h.healthMux.Lock()
	defer h.healthMux.Unlock()
	h.healthzChecksInstalled = true
	InstallPathHandler(lg, mux, "/healthz", h.getHealthChecksByNames(h.healthzChecks)...)
}

// installReadyz creates the readyz endpoint for this server.
func (h *HealthHandler) installReadyz(lg *zap.Logger, mux mux) {
	h.healthMux.Lock()
	defer h.healthMux.Unlock()
	h.readyzChecksInstalled = true
	InstallPathHandler(lg, mux, "/readyz", h.getHealthChecksByNames(h.readyzChecks)...)
}

// installLivez creates the livez endpoint for this server.
func (h *HealthHandler) installLivez(lg *zap.Logger, mux mux) {
	h.healthMux.Lock()
	defer h.healthMux.Unlock()
	h.livezChecksInstalled = true
	InstallPathHandler(lg, mux, "/livez", h.getHealthChecksByNames(h.livezChecks)...)
}

// InstallPathHandler registers handlers for health checking on
// a specific path to mux. *All handlers* for the path must be
// specified in exactly one call to InstallPathHandler. Calling
// InstallPathHandler more than once for the same path and mux will
// result in a panic.
func InstallPathHandler(lg *zap.Logger, mux mux, path string, checks ...HealthChecker) {
	if len(checks) == 0 {
		lg.Info("No default health checks specified. Installing the ping handler.")
		checks = []HealthChecker{PingHealthz}
	}

	lg.Sugar().Infof("Installing health checkers for (%v): %v", path, formatQuoted(checkerNames(checks...)...))

	name := strings.Split(strings.TrimPrefix(path, "/"), "/")[0]
	mux.Handle(path,
		handleRootHealth(lg, name, checks...))
	for _, check := range checks {
		mux.Handle(fmt.Sprintf("%s/%v", path, check.Name()), adaptCheckToHandler(check.Check))
	}
}

// mux is an interface describing the methods InstallHandler requires.
type mux interface {
	Handle(pattern string, handler http.Handler)
}

// getChecksForQuery extracts the health check names from the query param
func getChecksForQuery(r *http.Request, query string) stringSet {
	checksSet := make(map[string]struct{}, 2)
	checks, found := r.URL.Query()[query]
	if found {
		for _, chk := range checks {
			if len(chk) == 0 {
				continue
			}
			checksSet[chk] = struct{}{}
		}
	}
	return checksSet
}

// handleRootHealth returns an http.HandlerFunc that serves the provided checks.
func handleRootHealth(lg *zap.Logger, name string, checks ...HealthChecker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// extracts the health check names to be excluded from the query param
		excluded := getChecksForQuery(r, "exclude")
		// extracts the health check names to be included from the query param
		included := getChecksForQuery(r, "allowlist")
		if len(excluded) > 0 && len(included) > 0 {
			lg.Sugar().Infof("do not expect both allowlist and exclude to be specified in the query %v", r.URL.RawQuery)
			http.Error(w, fmt.Sprintf("do not expect both allowlist and exclude to be specified in the query %v", r.URL.RawQuery), http.StatusBadRequest)
			return
		}
		isAllowList := len(included) > 0
		// failedVerboseLogOutput is for output to the log.  It indicates detailed failed output information for the log.
		var failedVerboseLogOutput bytes.Buffer
		var failedChecks []string
		var individualCheckOutput bytes.Buffer
		for _, check := range checks {
			if isAllowList {
				if _, found := included[check.Name()]; !found {
					fmt.Fprintf(&individualCheckOutput, "[+]%s not included: ok\n", check.Name())
					continue
				}
				delete(included, check.Name())
			} else {
				// no-op the check if we've specified we want to exclude the check
				if _, found := excluded[check.Name()]; found {
					delete(excluded, check.Name())
					fmt.Fprintf(&individualCheckOutput, "[+]%s excluded: ok\n", check.Name())
					continue
				}
			}
			if err := check.Check(r); err != nil {
				// don't include the error since this endpoint is public.  If someone wants more detail
				// they should have explicit permission to the detailed checks.
				fmt.Fprintf(&individualCheckOutput, "[-]%s failed: reason withheld\n", check.Name())
				// but we do want detailed information for our log
				fmt.Fprintf(&failedVerboseLogOutput, "[-]%s failed: %v\n", check.Name(), err)
				failedChecks = append(failedChecks, check.Name())
			} else {
				fmt.Fprintf(&individualCheckOutput, "[+]%s ok\n", check.Name())
			}
		}
		if len(excluded) > 0 {
			fmt.Fprintf(&individualCheckOutput, "warn: some health checks cannot be excluded: no matches for %s\n", formatQuoted(excluded.List()...))
			lg.Sugar().Infof("cannot exclude some health checks, no health checks are installed matching %s",
				formatQuoted(excluded.List()...))
		}
		if len(included) > 0 {
			fmt.Fprintf(&individualCheckOutput, "warn: some health checks cannot be included: no matches for %s\n", formatQuoted(included.List()...))
			lg.Sugar().Infof("cannot include some health checks, no health checks are installed matching %s",
				formatQuoted(included.List()...))
		}
		// always be verbose on failure
		if len(failedChecks) > 0 {
			lg.Sugar().Infof("%s check failed: %s\n%v", strings.Join(failedChecks, ","), name, failedVerboseLogOutput.String())
			http.Error(w, fmt.Sprintf("%s%s check failed", individualCheckOutput.String(), name), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if _, found := r.URL.Query()["verbose"]; !found {
			fmt.Fprint(w, "ok")
			return
		}

		individualCheckOutput.WriteTo(w)
		fmt.Fprintf(w, "%s check passed\n", name)
	}
}

// adaptCheckToHandler returns an http.HandlerFunc that serves the provided checks.
func adaptCheckToHandler(c func(r *http.Request) error) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := c(r)
		if err != nil {
			http.Error(w, fmt.Sprintf("internal server error: %v", err), http.StatusInternalServerError)
		} else {
			fmt.Fprint(w, "ok")
		}
	})
}

// checkerNames returns the names of the checks in the same order as passed in.
func checkerNames(checks ...HealthChecker) []string {
	// accumulate the names of checks for printing them out.
	checkerNames := make([]string, 0, len(checks))
	for _, check := range checks {
		checkerNames = append(checkerNames, check.Name())
	}
	return checkerNames
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
