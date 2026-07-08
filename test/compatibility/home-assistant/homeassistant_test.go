// Copyright 2026 The gVisor Authors.
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

// Package homeassistant is a gVisor compatibility test for Home Assistant.
//
// The version under test is pinned in
// images/compatibility/home-assistant/home-assistant/Dockerfile.
package homeassistant

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/test/dockerutil"
	"gvisor.dev/gvisor/test/compatibility"
)

const (
	haImage = "compatibility/home-assistant/home-assistant"

	ownerName     = "gVisor Test"
	ownerUsername = "gvadmin"
	ownerPassword = "gvisor-Test-1234"

	haPort = 8123

	readyTimeout = 5 * time.Minute // first-run init is slow, more so under gVisor.
	pollInterval = 2 * time.Second
)

func TestHomeAssistant(t *testing.T) {
	ctx := context.Background()

	ha := dockerutil.MakeContainer(ctx, t)
	defer ha.CleanUp(ctx)
	if err := ha.Spawn(ctx, dockerutil.RunOpts{Image: haImage}); err != nil {
		t.Fatalf("failed to start home assistant: %v", err)
	}

	ip, err := ha.FindIP(ctx, false)
	if err != nil {
		t.Fatalf("failed to find home assistant IP: %v", err)
	}
	base := fmt.Sprintf("http://%s:%d", ip.String(), haPort)
	clientID := base + "/"

	// Wait until onboarding is being served.
	compatibility.Poll(ctx, t, "home assistant onboarding to be ready", readyTimeout, pollInterval, func() error {
		status, body, err := compatibility.Get(base + "/api/onboarding")
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("GET /api/onboarding: status %d", status)
		}
		if !strings.Contains(body, `"step"`) {
			return fmt.Errorf("onboarding not ready: %s", body)
		}
		return nil
	})

	// Create the owner user; returns an auth code.
	setup := fmt.Sprintf(
		`{"client_id":%q,"name":%q,"username":%q,"password":%q,"language":"en"}`,
		clientID, ownerName, ownerUsername, ownerPassword)
	body := compatibility.Request{
		Method:      http.MethodPost,
		URL:         base + "/api/onboarding/users",
		ContentType: "application/json",
		Body:        setup,
	}.DoOrFatal(t, http.StatusOK)
	var setupResp struct {
		AuthCode string `json:"auth_code"`
	}
	if err := json.Unmarshal([]byte(body), &setupResp); err != nil || setupResp.AuthCode == "" {
		t.Fatalf("onboarding users: no auth_code in response (err=%v): %s", err, body)
	}

	// Exchange the auth code for an access token.
	form := url.Values{
		"grant_type": {"authorization_code"},
		"code":       {setupResp.AuthCode},
		"client_id":  {clientID},
	}
	body = compatibility.Request{
		Method:      http.MethodPost,
		URL:         base + "/auth/token",
		ContentType: "application/x-www-form-urlencoded",
		Body:        form.Encode(),
	}.DoOrFatal(t, http.StatusOK)
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal([]byte(body), &tok); err != nil || tok.AccessToken == "" {
		t.Fatalf("token exchange: no access_token in response (err=%v): %s", err, body)
	}
	auth := map[string]string{"Authorization": "Bearer " + tok.AccessToken}

	// The API should report it is running.
	if body := (compatibility.Request{URL: base + "/api/", Headers: auth}).DoOrFatal(t, http.StatusOK); !strings.Contains(body, "API running") {
		t.Fatalf("GET /api/: unexpected body %s", body)
	}

	// Wait for the instance to finish starting (RUNNING state).
	compatibility.Poll(ctx, t, "home assistant to reach RUNNING state", readyTimeout, pollInterval, func() error {
		status, body, err := compatibility.Request{URL: base + "/api/config", Headers: auth}.Do()
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("GET /api/config: status %d", status)
		}
		if !strings.Contains(body, `"state":"RUNNING"`) {
			return fmt.Errorf("not RUNNING yet")
		}
		return nil
	})

	// Create an entity state (a write through the SQLite recorder / state machine).
	const stateValue = "42"
	compatibility.Request{
		Method:      http.MethodPost,
		URL:         base + "/api/states/sensor.gvtest",
		ContentType: "application/json",
		Body:        fmt.Sprintf(`{"state":%q,"attributes":{"unit_of_measurement":"gv"}}`, stateValue),
		Headers:     auth,
	}.DoOrFatal(t, http.StatusCreated)

	// Read it back.
	body = compatibility.Request{URL: base + "/api/states/sensor.gvtest", Headers: auth}.DoOrFatal(t, http.StatusOK)
	var st struct {
		EntityID string `json:"entity_id"`
		State    string `json:"state"`
	}
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		t.Fatalf("read state: bad JSON (%v): %s", err, body)
	}
	if st.EntityID != "sensor.gvtest" || st.State != stateValue {
		t.Fatalf("read state: got entity %q state %q, want %q / %q", st.EntityID, st.State, "sensor.gvtest", stateValue)
	}
}

func TestMain(m *testing.M) {
	dockerutil.EnsureSupportedDockerVersion()
	flag.Parse()
	os.Exit(m.Run())
}
