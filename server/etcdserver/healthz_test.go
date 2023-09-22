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

package etcdserver

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"go.uber.org/zap/zaptest"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/client/pkg/v3/testutil"
	"go.etcd.io/etcd/server/v3/auth"
)

type fakeHealthServer struct {
	apiError error
	alarms   []*pb.AlarmMember
}

func (s *fakeHealthServer) Range(ctx context.Context, request *pb.RangeRequest) (*pb.RangeResponse, error) {
	return nil, s.apiError
}

func (s *fakeHealthServer) Alarms() []*pb.AlarmMember { return s.alarms }

func (s *fakeHealthServer) ClientCertAuthEnabled() bool { return false }

type healthzTestCase struct {
	name             string
	healthCheckURL   string
	expectStatusCode int
	inResult         []string
	notInResult      []string

	alarms   []*pb.AlarmMember
	apiError error
}

// Test the basic logic flow in the handler.
func TestHealthHandler(t *testing.T) {
	mux := http.NewServeMux()
	handler := &HealthHandler{
		server:           &fakeHealthServer{},
		healthCheckStore: make(map[string]HealthChecker),
		healthzChecks:    []string{},
		livezChecks:      []string{},
		readyzChecks:     []string{},
	}
	logger := zaptest.NewLogger(t)
	// Some helper functions
	failedFunc := func(r *http.Request) error { return fmt.Errorf("Failed") }
	succeededFunc := func(r *http.Request) error { return nil }
	ableToAddCheck := func(expectSuccess bool, chk HealthChecker, isLivez bool, isReadyz bool) {
		err := handler.AddHealthCheck(chk, isLivez, isReadyz)
		if expectSuccess && err != nil {
			t.Errorf("Expect being able to add check %s", chk.Name())
		}
		if !expectSuccess && err == nil {
			t.Errorf("Expect not being able to add check %s", chk.Name())
		}
	}
	ableToAddCheck(true, NamedCheck("livez_only_1", succeededFunc), true, false)
	ableToAddCheck(false, NamedCheck("livez_only_1", failedFunc), true, false)
	ableToAddCheck(true, NamedCheck("livez_only_2", failedFunc), true, false)
	ableToAddCheck(true, NamedCheck("livez_readyz_1", succeededFunc), true, true)

	handler.installLivez(logger, mux)

	ableToAddCheck(false, NamedCheck("livez_only_3", succeededFunc), true, false)
	ableToAddCheck(false, NamedCheck("livez_readyz_2", succeededFunc), true, true)
	ableToAddCheck(true, NamedCheck("readyz_only_1", succeededFunc), false, true)
	ableToAddCheck(true, NamedCheck("readyz_only_2", failedFunc), false, true)

	handler.installReadyz(logger, mux)

	ableToAddCheck(false, NamedCheck("readyz_only_3", succeededFunc), false, true)
	ableToAddCheck(true, NamedCheck("neither_1", failedFunc), false, false)

	handler.installHealthz(logger, mux)
	ableToAddCheck(false, NamedCheck("neither_2", succeededFunc), false, false)

	expectedLivezChecks := []string{"livez_only_1", "livez_only_2", "livez_readyz_1"}
	if !reflect.DeepEqual(expectedLivezChecks, handler.livezChecks) {
		t.Errorf("expectedLivezChecks: %v, but got: %v", expectedLivezChecks, handler.livezChecks)
		return
	}
	expectedReadyzChecks := []string{"livez_readyz_1", "readyz_only_1", "readyz_only_2"}
	if !reflect.DeepEqual(expectedReadyzChecks, handler.readyzChecks) {
		t.Errorf("expectedReadyzChecks: %v, but got: %v", expectedReadyzChecks, handler.readyzChecks)
		return
	}
	expectedHealthzChecks := []string{"livez_only_1", "livez_only_2", "livez_readyz_1", "readyz_only_1", "readyz_only_2", "neither_1"}
	if !reflect.DeepEqual(expectedHealthzChecks, handler.healthzChecks) {
		t.Errorf("expectedHealthzChecks: %v, but got: %v", expectedHealthzChecks, handler.healthzChecks)
		return
	}
	ts := httptest.NewServer(mux)
	defer ts.Close()

	tests := []healthzTestCase{
		{
			name:             "livez not ok by default",
			healthCheckURL:   "/livez",
			expectStatusCode: http.StatusInternalServerError,
			inResult:         []string{"[+]livez_only_1 ok", "[-]livez_only_2 failed: reason withheld", "[+]livez_readyz_1 ok", "livez check failed"},
			notInResult:      []string{"readyz_only_"},
		},
		{
			name:             "readyz not ok by default",
			healthCheckURL:   "/readyz",
			expectStatusCode: http.StatusInternalServerError,
			inResult:         []string{"[+]readyz_only_1 ok", "[-]readyz_only_2 failed: reason withheld", "[+]livez_readyz_1 ok", "readyz check failed"},
			notInResult:      []string{"livez_only_"},
		},
		{
			name:             "healthz not ok by default",
			healthCheckURL:   "/healthz",
			expectStatusCode: http.StatusInternalServerError,
			inResult:         []string{"[+]livez_only_1 ok", "[-]livez_only_2 failed: reason withheld", "[+]readyz_only_1 ok", "[-]readyz_only_2 failed: reason withheld", "[+]livez_readyz_1 ok", "[-]neither_1 failed: reason withheld", "healthz check failed"},
		},
		{
			name:             "livez ok if exclude livez_only_2",
			healthCheckURL:   "/livez?exclude=livez_only_2",
			expectStatusCode: http.StatusOK,
		},
		{
			name:             "livez ok if allowlist livez_only_1, livez_readyz_1",
			healthCheckURL:   "/livez?verbose&allowlist=livez_only_1&allowlist=livez_readyz_1&allowlist=readyz_only_2",
			expectStatusCode: http.StatusOK,
			inResult:         []string{"[+]livez_only_1 ok", "[+]livez_only_2 not included: ok", "[+]livez_readyz_1 ok", "warn: some health checks cannot be included: no matches for \"readyz_only_2\"", "livez check passed"},
		},
		{
			name:             "livez/livez_only_1 ok",
			healthCheckURL:   "/livez/livez_only_1",
			expectStatusCode: http.StatusOK,
		},
		{
			name:             "livez/livez_only_2 not ok",
			healthCheckURL:   "/livez/livez_only_2",
			expectStatusCode: http.StatusInternalServerError,
		},
		{
			name:             "livez/readyz_only_1 not a good url",
			healthCheckURL:   "/livez/readyz_only_1",
			expectStatusCode: http.StatusNotFound,
		},
		{
			name:             "cannot specify both exclude and allowlist",
			healthCheckURL:   "/livez?allowlist=livez_only_1&exclude=livez_readyz_1",
			expectStatusCode: http.StatusBadRequest,
		},
		{
			name:             "readyz ok if allowlist readyz_only_1",
			healthCheckURL:   "/readyz?verbose&allowlist=livez_only_2&allowlist=readyz_only_1&allowlist=neither_1",
			expectStatusCode: http.StatusOK,
			inResult:         []string{"[+]readyz_only_1 ok", "[+]readyz_only_2 not included: ok", "[+]livez_readyz_1 not included: ok", "readyz check passed"},
			notInResult:      []string{"]livez_only_2", "]neither_1"},
		},
		{
			name:             "could allowlist both livez and healthz checks in healthz",
			healthCheckURL:   "/healthz?allowlist=livez_only_1&allowlist=readyz_only_1&allowlist=neither_1",
			expectStatusCode: http.StatusInternalServerError,
			inResult:         []string{"[+]livez_only_1 ok", "[+]readyz_only_1 ok", "[+]livez_only_2 not included: ok", "[+]livez_readyz_1 not included: ok", "[-]neither_1 failed: reason withheld", "healthz check failed"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkHttpResponse(t, ts, tt.healthCheckURL, tt.expectStatusCode, tt.inResult, tt.notInResult)
		})
	}
}

func TestDataCorruptionCheck(t *testing.T) {
	tests := []healthzTestCase{
		{
			name:             "Live if CORRUPT alarm is on",
			alarms:           []*pb.AlarmMember{{MemberID: uint64(0), Alarm: pb.AlarmType_CORRUPT}},
			healthCheckURL:   "/livez",
			expectStatusCode: http.StatusOK,
		},
		{
			name:             "Not ready if CORRUPT alarm is on",
			alarms:           []*pb.AlarmMember{{MemberID: uint64(0), Alarm: pb.AlarmType_CORRUPT}},
			healthCheckURL:   "/readyz",
			expectStatusCode: http.StatusInternalServerError,
			inResult:         []string{"[-]data_corruption failed: reason withheld", "[+]ping ok", "readyz check failed"},
		},
		{
			name:             "healthz not ok if CORRUPT alarm is on",
			alarms:           []*pb.AlarmMember{{MemberID: uint64(0), Alarm: pb.AlarmType_CORRUPT}},
			healthCheckURL:   "/healthz",
			expectStatusCode: http.StatusInternalServerError,
			inResult:         []string{"[-]data_corruption failed: reason withheld", "[+]ping ok", "healthz check failed"},
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
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			s := fakeHealthServer{}
			handler, _ := NewHealthHandler(&s)
			logger := zaptest.NewLogger(t)
			handler.installLivez(logger, mux)
			handler.installReadyz(logger, mux)
			handler.installHealthz(logger, mux)
			ts := httptest.NewServer(mux)
			defer ts.Close()
			checkHttpResponse(t, ts, tt.healthCheckURL, http.StatusOK, nil, nil)
			// Activate the alarms.
			s.alarms = tt.alarms
			checkHttpResponse(t, ts, tt.healthCheckURL, tt.expectStatusCode, tt.inResult, tt.notInResult)
		})
	}
}

func TestSerializableReadCheck(t *testing.T) {
	tests := []healthzTestCase{
		{
			name:             "Alive even if authentication failed",
			healthCheckURL:   "/livez",
			apiError:         auth.ErrUserEmpty,
			expectStatusCode: http.StatusOK,
		},
		{
			name:             "Alive even if authorization failed",
			healthCheckURL:   "/livez",
			apiError:         auth.ErrPermissionDenied,
			expectStatusCode: http.StatusOK,
		},
		{
			name:             "Not alive if range api is not available",
			healthCheckURL:   "/livez",
			apiError:         fmt.Errorf("Unexpected error"),
			expectStatusCode: http.StatusInternalServerError,
		},
		{
			name:             "Not ready if range api is not available",
			healthCheckURL:   "/readyz",
			apiError:         fmt.Errorf("Unexpected error"),
			expectStatusCode: http.StatusInternalServerError,
		},
		{
			name:             "Unhealthy if range api is not available",
			healthCheckURL:   "/healthz",
			apiError:         fmt.Errorf("Unexpected error"),
			expectStatusCode: http.StatusInternalServerError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			s := fakeHealthServer{apiError: tt.apiError}
			handler, _ := NewHealthHandler(&s)
			logger := zaptest.NewLogger(t)
			handler.installLivez(logger, mux)
			handler.installReadyz(logger, mux)
			handler.installHealthz(logger, mux)
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
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Errorf("Failed to read response for %s", url)
		return
	}
	result := string(b)
	for _, substr := range inResult {
		if !strings.Contains(result, substr) {
			t.Errorf("Could not find substring : %s, in response: %s", substr, result)
			return
		}
	}
	for _, substr := range notInResult {
		if strings.Contains(result, substr) {
			t.Errorf("Do not expect substring : %s, in response: %s", substr, result)
			return
		}
	}
}
