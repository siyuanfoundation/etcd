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

// Test the basic logic flow in the handler.
func TestHealthHandler(t *testing.T) {
	mux := http.NewServeMux()
	s := fakeHealthServer{}
	handler, _ := NewHealthHandler(&s)
	logger := zaptest.NewLogger(t)
	failedFunc := func(r *http.Request) error { return fmt.Errorf("Failed") }
	succeededFunc := func(r *http.Request) error { return nil }
	if err := handler.AddHealthCheck(NamedCheck("livez_only_1", succeededFunc), true, false); err != nil {
		t.Errorf("failed to add livez check")
		return
	}
	if err := handler.AddHealthCheck(NamedCheck("livez_only_1", succeededFunc), false, false); err == nil {
		t.Errorf("failed to check duplicate check name")
	}
	if err := handler.AddHealthCheck(NamedCheck("livez_readyz_1", succeededFunc), true, true); err != nil {
		t.Errorf("failed to add readyz+livez check")
		return
	}
	handler.installLivez(logger, mux)
	if err := handler.AddHealthCheck(NamedCheck("livez_only_2", failedFunc), true, false); err == nil {
		t.Errorf("should not be able to add more livez checks after installLivez")
		return
	}
	if err := handler.AddHealthCheck(NamedCheck("livez_readyz_2", failedFunc), true, true); err == nil {
		t.Errorf("should not be able to add more livez checks after installLivez")
		return
	}
	if err := handler.AddHealthCheck(NamedCheck("readyz_only_1", succeededFunc), false, true); err != nil {
		t.Errorf("failed to add readyz check")
		return
	}
	handler.installReadyz(logger, mux)
	if err := handler.AddHealthCheck(NamedCheck("readyz_only_2", failedFunc), false, true); err == nil {
		t.Errorf("should not be able to add more readyz checks after installReadyz")
		return
	}
	if err := handler.AddHealthCheck(NamedCheck("livez_readyz_2", failedFunc), true, true); err == nil {
		t.Errorf("should not be able to add more readyz checks after installReadyz")
		return
	}
	if err := handler.AddHealthCheck(NamedCheck("non_livez_readyz_1", succeededFunc), false, false); err != nil {
		t.Errorf("failed to add only healthz check")
		return
	}
	handler.installHealthz(logger, mux)
	if err := handler.AddHealthCheck(NamedCheck("non_livez_readyz_2", failedFunc), false, false); err == nil {
		t.Errorf("should not be able to add more healthz checks after installHealthz")
		return
	}
	ts := httptest.NewServer(mux)
	defer ts.Close()

	for _, healthCheckURL := range []string{"/livez", "/readyz", "/healthz"} {
		checkHttpResponse(t, ts, healthCheckURL, http.StatusOK, nil, nil)
	}
	// Activate the alarms.
	s.alarms = []*pb.AlarmMember{{MemberID: uint64(0), Alarm: pb.AlarmType_CORRUPT}}
	tests := []struct {
		name             string
		healthCheckURL   string
		expectStatusCode int
		inResult         []string
		notInResult      []string
	}{
		{
			name:             "Live if CORRUPT alarm is on",
			healthCheckURL:   "/livez",
			expectStatusCode: http.StatusOK,
		},
		{
			name:             "Not ready if CORRUPT alarm is on",
			healthCheckURL:   "/readyz",
			expectStatusCode: http.StatusInternalServerError,
			inResult:         []string{"[-]data_corruption failed: reason withheld", "[+]livez_readyz_1 ok", "[+]readyz_only_1 ok", "readyz check failed"},
			notInResult:      []string{"livez_only_"},
		},
		{
			name:             "Not healthy if CORRUPT alarm is on",
			healthCheckURL:   "/healthz",
			expectStatusCode: http.StatusInternalServerError,
			inResult:         []string{"[-]data_corruption failed: reason withheld", "[+]livez_only_1 ok", "[+]livez_readyz_1 ok", "[+]readyz_only_1 ok", "[+]non_livez_readyz_1 ok", "healthz check failed"},
		},
	}
	for _, tt := range tests {
		checkHttpResponse(t, ts, tt.healthCheckURL, tt.expectStatusCode, tt.inResult, tt.notInResult)
	}
}

func TestHealthzEndpoints(t *testing.T) {
	// define the input and expected output
	// input: alarms, and healthCheckURL
	tests := []struct {
		name           string
		alarms         []*pb.AlarmMember
		healthCheckURL string
		apiError       error

		expectStatusCode int
	}{
		{
			name:             "Healthy if no alarm",
			alarms:           []*pb.AlarmMember{},
			healthCheckURL:   "/healthz",
			expectStatusCode: http.StatusOK,
		},
		{
			name:             "Unhealthy if CORRUPT alarm is on",
			alarms:           []*pb.AlarmMember{{MemberID: uint64(0), Alarm: pb.AlarmType_CORRUPT}},
			healthCheckURL:   "/healthz",
			expectStatusCode: http.StatusInternalServerError,
		},
		{
			name:             "Healthy if CORRUPT alarm is on and excluded",
			alarms:           []*pb.AlarmMember{{MemberID: uint64(0), Alarm: pb.AlarmType_CORRUPT}},
			healthCheckURL:   "/healthz?exclude=data_corruption",
			expectStatusCode: http.StatusOK,
		},
		{
			name:             "Alive if CORRUPT alarm is on",
			alarms:           []*pb.AlarmMember{{MemberID: uint64(0), Alarm: pb.AlarmType_CORRUPT}},
			healthCheckURL:   "/livez",
			expectStatusCode: http.StatusOK,
		},
		{
			name:             "Not ready if CORRUPT alarm is on",
			alarms:           []*pb.AlarmMember{{MemberID: uint64(0), Alarm: pb.AlarmType_CORRUPT}},
			healthCheckURL:   "/readyz",
			expectStatusCode: http.StatusInternalServerError,
		},
		{
			name:             "Ready if CORRUPT alarm is on and excluded",
			alarms:           []*pb.AlarmMember{{MemberID: uint64(0), Alarm: pb.AlarmType_CORRUPT}},
			healthCheckURL:   "/readyz?exclude=data_corruption",
			expectStatusCode: http.StatusOK,
		},
		{
			name:             "subpath ping ok if CORRUPT alarm is on",
			alarms:           []*pb.AlarmMember{{MemberID: uint64(0), Alarm: pb.AlarmType_CORRUPT}},
			healthCheckURL:   "/readyz/ping",
			expectStatusCode: http.StatusOK,
		},
		{
			name:             "subpath data_corruption not ok if CORRUPT alarm is on",
			alarms:           []*pb.AlarmMember{{MemberID: uint64(0), Alarm: pb.AlarmType_CORRUPT}},
			healthCheckURL:   "/readyz/data_corruption",
			expectStatusCode: http.StatusInternalServerError,
		},
		{
			name:             "Healthy if CORRUPT alarm is excluded",
			alarms:           []*pb.AlarmMember{},
			healthCheckURL:   "/healthz?exclude=data_corruption",
			expectStatusCode: http.StatusOK,
		},
		{
			name:             "Ready if multiple NOSPACE alarms are on",
			alarms:           []*pb.AlarmMember{{MemberID: uint64(1), Alarm: pb.AlarmType_NOSPACE}, {MemberID: uint64(2), Alarm: pb.AlarmType_NOSPACE}, {MemberID: uint64(3), Alarm: pb.AlarmType_NOSPACE}},
			healthCheckURL:   "/readyz",
			expectStatusCode: http.StatusOK,
		},
		{
			name:             "Bad request if both exclude and allowlist are specified",
			alarms:           []*pb.AlarmMember{{MemberID: uint64(0), Alarm: pb.AlarmType_NOSPACE}, {MemberID: uint64(1), Alarm: pb.AlarmType_CORRUPT}},
			healthCheckURL:   "/healthz?exclude=ping&allowlist=data_corruption",
			expectStatusCode: http.StatusBadRequest,
		},
		{
			name:             "Healthy even if authentication failed",
			healthCheckURL:   "/healthz",
			apiError:         auth.ErrUserEmpty,
			expectStatusCode: http.StatusOK,
		},
		{
			name:             "Healthy even if authorization failed",
			healthCheckURL:   "/healthz",
			apiError:         auth.ErrPermissionDenied,
			expectStatusCode: http.StatusOK,
		},
		{
			name:             "Unhealthy if range api is not available",
			healthCheckURL:   "/livez",
			apiError:         fmt.Errorf("Unexpected error"),
			expectStatusCode: http.StatusInternalServerError,
		},
		{
			name:             "Unhealthy if range api is not available",
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
			handler, _ := NewHealthHandler(&fakeHealthServer{
				alarms:   tt.alarms,
				apiError: tt.apiError,
			})
			logger := zaptest.NewLogger(t)
			handler.installLivez(logger, mux)
			handler.installReadyz(logger, mux)
			handler.installHealthz(logger, mux)
			ts := httptest.NewServer(mux)
			defer ts.Close()

			checkHttpResponse(t, ts, tt.healthCheckURL, tt.expectStatusCode, nil, nil)
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
