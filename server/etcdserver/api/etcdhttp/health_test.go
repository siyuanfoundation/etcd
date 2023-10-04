// Copyright 2022 The etcd Authors
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
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap/zaptest"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/client/pkg/v3/testutil"
	"go.etcd.io/etcd/client/pkg/v3/types"
	"go.etcd.io/etcd/server/v3/auth"
	"go.etcd.io/etcd/server/v3/config"
	betesting "go.etcd.io/etcd/server/v3/storage/backend/testing"
	"go.etcd.io/etcd/server/v3/storage/schema"
	"go.etcd.io/raft/v3"
)

type fakeHealthServer struct {
	fakeServer
	apiError      error
	missingLeader bool
	authStore     auth.AuthStore
}

func (s *fakeHealthServer) Range(ctx context.Context, request *pb.RangeRequest) (*pb.RangeResponse, error) {
	return nil, s.apiError
}

func (s *fakeHealthServer) Config() config.ServerConfig {
	return config.ServerConfig{}
}

func (s *fakeHealthServer) Leader() types.ID {
	if !s.missingLeader {
		return 1
	}
	return types.ID(raft.None)
}

func (s *fakeHealthServer) AuthStore() auth.AuthStore {
	return s.authStore
}

func (s *fakeHealthServer) ClientCertAuthEnabled() bool { return false }

type healthTestCase struct {
	name             string
	healthCheckURL   string
	expectStatusCode int
	inResult         []string
	notInResult      []string

	alarms        []*pb.AlarmMember
	apiError      error
	missingLeader bool
}

func TestHealthHandler(t *testing.T) {
	// define the input and expected output
	// input: alarms, and healthCheckURL
	tests := []healthTestCase{
		{
			name:             "Healthy if no alarm",
			alarms:           []*pb.AlarmMember{},
			healthCheckURL:   "/health",
			expectStatusCode: http.StatusOK,
		},
		{
			name:             "Unhealthy if NOSPACE alarm is on",
			alarms:           []*pb.AlarmMember{{MemberID: uint64(0), Alarm: pb.AlarmType_NOSPACE}},
			healthCheckURL:   "/health",
			expectStatusCode: http.StatusServiceUnavailable,
		},
		{
			name:             "Healthy if NOSPACE alarm is on and excluded",
			alarms:           []*pb.AlarmMember{{MemberID: uint64(0), Alarm: pb.AlarmType_NOSPACE}},
			healthCheckURL:   "/health?exclude=NOSPACE",
			expectStatusCode: http.StatusOK,
		},
		{
			name:             "Healthy if NOSPACE alarm is excluded",
			alarms:           []*pb.AlarmMember{},
			healthCheckURL:   "/health?exclude=NOSPACE",
			expectStatusCode: http.StatusOK,
		},
		{
			name:             "Healthy if multiple NOSPACE alarms are on and excluded",
			alarms:           []*pb.AlarmMember{{MemberID: uint64(1), Alarm: pb.AlarmType_NOSPACE}, {MemberID: uint64(2), Alarm: pb.AlarmType_NOSPACE}, {MemberID: uint64(3), Alarm: pb.AlarmType_NOSPACE}},
			healthCheckURL:   "/health?exclude=NOSPACE",
			expectStatusCode: http.StatusOK,
		},
		{
			name:             "Unhealthy if NOSPACE alarms is excluded and CORRUPT is on",
			alarms:           []*pb.AlarmMember{{MemberID: uint64(0), Alarm: pb.AlarmType_NOSPACE}, {MemberID: uint64(1), Alarm: pb.AlarmType_CORRUPT}},
			healthCheckURL:   "/health?exclude=NOSPACE",
			expectStatusCode: http.StatusServiceUnavailable,
		},
		{
			name:             "Unhealthy if both NOSPACE and CORRUPT are on and excluded",
			alarms:           []*pb.AlarmMember{{MemberID: uint64(0), Alarm: pb.AlarmType_NOSPACE}, {MemberID: uint64(1), Alarm: pb.AlarmType_CORRUPT}},
			healthCheckURL:   "/health?exclude=NOSPACE&exclude=CORRUPT",
			expectStatusCode: http.StatusOK,
		},
		{
			name:             "Healthy even if authentication failed",
			healthCheckURL:   "/health",
			apiError:         auth.ErrUserEmpty,
			expectStatusCode: http.StatusOK,
		},
		{
			name:             "Healthy even if authorization failed",
			healthCheckURL:   "/health",
			apiError:         auth.ErrPermissionDenied,
			expectStatusCode: http.StatusOK,
		},
		{
			name:             "Unhealthy if api is not available",
			healthCheckURL:   "/health",
			apiError:         fmt.Errorf("Unexpected error"),
			expectStatusCode: http.StatusServiceUnavailable,
		},
		{
			name:             "Unhealthy if no leader",
			healthCheckURL:   "/health",
			expectStatusCode: http.StatusServiceUnavailable,
			missingLeader:    true,
			notInResult:      []string{"serializable_read"},
		},
		{
			name:             "Healthy if no leader and serializable=true",
			healthCheckURL:   "/health?serializable=true",
			expectStatusCode: http.StatusOK,
			missingLeader:    true,
			notInResult:      []string{"linearizable_read", "leader"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			HandleHealth(zaptest.NewLogger(t), mux, &fakeHealthServer{
				fakeServer:    fakeServer{alarms: tt.alarms},
				apiError:      tt.apiError,
				missingLeader: tt.missingLeader,
			})
			ts := httptest.NewServer(mux)
			defer ts.Close()
			checkHttpResponse(t, ts, tt.healthCheckURL, tt.expectStatusCode, nil, nil)
		})
	}
}

func TestDataCorruptionCheck(t *testing.T) {
	be, _ := betesting.NewDefaultTmpBackend(t)
	defer betesting.Close(t, be)
	tests := []healthTestCase{
		{
			name:             "Live if CORRUPT alarm is on",
			alarms:           []*pb.AlarmMember{{MemberID: uint64(0), Alarm: pb.AlarmType_CORRUPT}},
			healthCheckURL:   "/livez",
			expectStatusCode: http.StatusOK,
			notInResult:      []string{"data_corruption"},
		},
		{
			name:             "Not ready if CORRUPT alarm is on",
			alarms:           []*pb.AlarmMember{{MemberID: uint64(0), Alarm: pb.AlarmType_CORRUPT}},
			healthCheckURL:   "/readyz",
			expectStatusCode: http.StatusServiceUnavailable,
			inResult:         []string{"[-]data_corruption failed: Alarm active: CORRUPT", "[+]serializable_read ok"},
		},
		{
			name:             "ready if CORRUPT alarm is not on",
			alarms:           []*pb.AlarmMember{{MemberID: uint64(0), Alarm: pb.AlarmType_NOSPACE}},
			healthCheckURL:   "/readyz",
			expectStatusCode: http.StatusOK,
		},
		{
			name:             "ready if CORRUPT alarm is excluded",
			alarms:           []*pb.AlarmMember{{MemberID: uint64(0), Alarm: pb.AlarmType_CORRUPT}, {MemberID: uint64(0), Alarm: pb.AlarmType_NOSPACE}},
			healthCheckURL:   "/readyz?exclude=data_corruption",
			expectStatusCode: http.StatusOK,
			inResult:         []string{"[+]serializable_read ok"},
		},
		{
			name:             "ready if CORRUPT alarm is not allowlisted",
			alarms:           []*pb.AlarmMember{{MemberID: uint64(0), Alarm: pb.AlarmType_CORRUPT}, {MemberID: uint64(0), Alarm: pb.AlarmType_NOSPACE}},
			healthCheckURL:   "/readyz?allowlist=serializable_read",
			expectStatusCode: http.StatusOK,
			inResult:         []string{"[+]serializable_read ok"},
		},
		{
			name:             "/ready/data_corruption error if CORRUPT alarm is on",
			alarms:           []*pb.AlarmMember{{MemberID: uint64(0), Alarm: pb.AlarmType_CORRUPT}, {MemberID: uint64(0), Alarm: pb.AlarmType_NOSPACE}},
			healthCheckURL:   "/readyz/data_corruption",
			expectStatusCode: http.StatusServiceUnavailable,
		},
		{
			name:             "/ready/serializable_read ok if CORRUPT alarm is on",
			alarms:           []*pb.AlarmMember{{MemberID: uint64(0), Alarm: pb.AlarmType_CORRUPT}, {MemberID: uint64(0), Alarm: pb.AlarmType_NOSPACE}},
			healthCheckURL:   "/readyz/serializable_read",
			expectStatusCode: http.StatusOK,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			logger := zaptest.NewLogger(t)
			s := &fakeHealthServer{
				authStore: auth.NewAuthStore(logger, schema.NewAuthBackend(logger, be), nil, 0),
			}
			HandleHealth(logger, mux, s)
			ts := httptest.NewServer(mux)
			defer ts.Close()
			// OK before alarms are activated.
			checkHttpResponse(t, ts, tt.healthCheckURL, http.StatusOK, nil, nil)
			// Activate the alarms.
			s.alarms = tt.alarms
			checkHttpResponse(t, ts, tt.healthCheckURL, tt.expectStatusCode, tt.inResult, tt.notInResult)
		})
	}
}

func TestSerializableReadCheck(t *testing.T) {
	be, _ := betesting.NewDefaultTmpBackend(t)
	defer betesting.Close(t, be)
	tests := []healthTestCase{
		{
			name:             "Alive normal",
			healthCheckURL:   "/livez",
			expectStatusCode: http.StatusOK,
		},
		{
			name:             "Not alive if authentication failed",
			healthCheckURL:   "/livez",
			apiError:         auth.ErrUserEmpty,
			expectStatusCode: http.StatusServiceUnavailable,
			inResult:         []string{"RANGE ERROR: auth: user name is empty"},
		},
		{
			name:             "Not alive if authorization failed",
			healthCheckURL:   "/livez",
			apiError:         auth.ErrPermissionDenied,
			expectStatusCode: http.StatusServiceUnavailable,
		},
		{
			name:             "Not alive if range api is not available",
			healthCheckURL:   "/livez",
			apiError:         fmt.Errorf("Unexpected error"),
			expectStatusCode: http.StatusServiceUnavailable,
		},
		{
			name:             "Not ready if range api is not available",
			healthCheckURL:   "/readyz",
			apiError:         fmt.Errorf("Unexpected error"),
			expectStatusCode: http.StatusServiceUnavailable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			logger := zaptest.NewLogger(t)
			s := &fakeHealthServer{
				apiError:  tt.apiError,
				authStore: auth.NewAuthStore(logger, schema.NewAuthBackend(logger, be), nil, 0),
			}
			HandleHealth(logger, mux, s)
			ts := httptest.NewServer(mux)
			defer ts.Close()
			checkHttpResponse(t, ts, tt.healthCheckURL, tt.expectStatusCode, tt.inResult, tt.notInResult)
		})
	}
}

func TestExcludeAndAllowlist(t *testing.T) {
	be, _ := betesting.NewDefaultTmpBackend(t)
	defer betesting.Close(t, be)
	tests := []healthTestCase{
		{
			name:             "Bad request if exclude a non registered check",
			healthCheckURL:   "/readyz?exclude=non_exist",
			expectStatusCode: http.StatusBadRequest,
		},
		{
			name:             "Bad request if allowlist a non registered check",
			healthCheckURL:   "/readyz?allowlist=non_exist",
			expectStatusCode: http.StatusBadRequest,
		},
		{
			name:             "Cannot specify both allowlist and exclude at the same time",
			healthCheckURL:   "/readyz?allowlist=data_corruption&exclude=serializable_read",
			apiError:         fmt.Errorf("Unexpected error"),
			expectStatusCode: http.StatusBadRequest,
		},
		{
			name:             "Not bad request just allowlist",
			healthCheckURL:   "/readyz?allowlist=data_corruption&allowlist=serializable_read",
			apiError:         fmt.Errorf("Unexpected error"),
			expectStatusCode: http.StatusServiceUnavailable,
		},
		{
			name:             "OK if allowlist check ok",
			healthCheckURL:   "/readyz?allowlist=data_corruption",
			apiError:         fmt.Errorf("Unexpected error"),
			expectStatusCode: http.StatusOK,
		},
		{
			name:             "OK if exclude check not ok",
			healthCheckURL:   "/readyz?exclude=serializable_read",
			apiError:         fmt.Errorf("Unexpected error"),
			expectStatusCode: http.StatusOK,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			logger := zaptest.NewLogger(t)
			s := &fakeHealthServer{
				apiError:  tt.apiError,
				authStore: auth.NewAuthStore(logger, schema.NewAuthBackend(logger, be), nil, 0),
			}
			HandleHealth(logger, mux, s)
			ts := httptest.NewServer(mux)
			defer ts.Close()
			checkHttpResponse(t, ts, tt.healthCheckURL, tt.expectStatusCode, tt.inResult, tt.notInResult)
		})
	}
}

func checkHttpResponse(t *testing.T, ts *httptest.Server, url string, expectStatusCode int, inResult []string, notInResult []string) {
	res, err := ts.Client().Do(&http.Request{Method: http.MethodGet, URL: testutil.MustNewURL(t, ts.URL+url)})

	if err != nil {
		t.Errorf("fail serve http request %s %v", url, err)
	}
	if res == nil {
		t.Errorf("got nil http response with http request %s", url)
		return
	}
	if res.StatusCode != expectStatusCode {
		t.Errorf("want statusCode %d but got %d", expectStatusCode, res.StatusCode)
	}
	if expectStatusCode == http.StatusBadRequest {
		return
	}
	health, err := parseHealthOutput(res.Body)
	if err != nil {
		t.Errorf("fail parse health check output %v", err)
	}
	expectHealth := "false"
	if expectStatusCode == http.StatusOK {
		expectHealth = "true"
	}
	if health.Health != expectHealth {
		t.Errorf("want health %s but got %v", expectHealth, health)
	}
	for _, substr := range inResult {
		if !strings.Contains(health.Reason, substr) {
			t.Errorf("Could not find substring : %s, in response: %s", substr, health.Reason)
			return
		}
	}
	for _, substr := range notInResult {
		if strings.Contains(health.Reason, substr) {
			t.Errorf("Do not expect substring : %s, in response: %s", substr, health.Reason)
			return
		}
	}
}

func parseHealthOutput(body io.Reader) (Health, error) {
	obj := Health{}
	d, derr := io.ReadAll(body)
	if derr != nil {
		return obj, derr
	}
	if err := json.Unmarshal(d, &obj); err != nil {
		return obj, err
	}
	return obj, nil
}
