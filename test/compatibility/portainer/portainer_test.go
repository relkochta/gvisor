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

// Package portainer is a gVisor compatibility test for Portainer.
//
// The Portainer and proxy versions are pinned in
// images/compatibility/portainer/{portainer,dockerproxy}/Dockerfile.
package portainer

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/mount"

	"gvisor.dev/gvisor/pkg/test/dockerutil"
	"gvisor.dev/gvisor/test/compatibility"
)

const (
	portainerImage = "compatibility/portainer/portainer"
	proxyImage     = "compatibility/portainer/dockerproxy"

	portainerPort = 9000
	proxyPort     = 2375
	proxyHost     = "dockerproxy" // link alias Portainer uses to reach the proxy.

	adminUser     = "admin"
	adminPassword = "gvisor-Test-1234!"

	readyTimeout = 3 * time.Minute
	pollInterval = 2 * time.Second
)

var (
	// ansiRE strips ANSI color codes from Portainer's (colorized) logs.
	ansiRE = regexp.MustCompile("\x1b\\[[0-9;]*m")
	// setupTokenRE matches the setup token Portainer prints on first start,
	// e.g. "setup_token=1a2b3c..." (after ANSI codes are stripped).
	setupTokenRE = regexp.MustCompile(`setup_token=([A-Za-z0-9]{6,})`)
)

func TestPortainer(t *testing.T) {
	ctx := context.Background()

	// The docker-socket-proxy runs unsandboxed.
	proxy := dockerutil.MakeNativeContainer(ctx, t)
	defer proxy.CleanUp(ctx)
	if err := proxy.Spawn(ctx, dockerutil.RunOpts{
		Image: proxyImage,
		Mounts: []mount.Mount{{
			Type:     mount.TypeBind,
			Source:   "/var/run/docker.sock",
			Target:   "/var/run/docker.sock",
			ReadOnly: true,
		}},
		Env: []string{
			"CONTAINERS=1", "IMAGES=1", "NETWORKS=1", "VOLUMES=1",
			"SERVICES=1", "TASKS=1", "NODES=1", "SWARM=1", "SYSTEM=1",
			"PING=1", "VERSION=1", "INFO=1", "EVENTS=1", "EXEC=1",
			"POST=1",
		},
	}); err != nil {
		t.Fatalf("failed to start docker-socket-proxy: %v", err)
	}

	proxyIP, err := proxy.FindIP(ctx, false)
	if err != nil {
		t.Fatalf("failed to find proxy IP: %v", err)
	}

	// Confirm the proxy can talk to the host Docker daemon before wiring up
	// Portainer.
	compatibility.Poll(ctx, t, "docker-socket-proxy to reach Docker", readyTimeout, pollInterval, func() error {
		status, body, err := compatibility.Get(fmt.Sprintf("http://%s:%d/version", proxyIP.String(), proxyPort))
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("proxy /version: status %d", status)
		}
		if !strings.Contains(body, "ApiVersion") {
			return fmt.Errorf("proxy /version: unexpected body %s", body)
		}
		return nil
	})

	// Portainer runs under the runtime being tested and reaches the proxy by link
	// name over TCP.
	p := dockerutil.MakeContainer(ctx, t)
	defer p.CleanUp(ctx)
	if err := p.Spawn(ctx, dockerutil.RunOpts{
		Image: portainerImage,
		Links: []string{proxy.MakeLink(proxyHost)},
	}); err != nil {
		t.Fatalf("failed to start portainer: %v", err)
	}

	pip, err := p.FindIP(ctx, false)
	if err != nil {
		t.Fatalf("failed to find portainer IP: %v", err)
	}
	base := fmt.Sprintf("http://%s:%d", pip.String(), portainerPort)

	// Wait for Portainer's API.
	compatibility.Poll(ctx, t, "portainer API to be ready", readyTimeout, pollInterval, func() error {
		status, _, err := compatibility.Get(base + "/api/system/status")
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("GET /api/system/status: status %d", status)
		}
		return nil
	})

	// Initialize the admin user. Recent Portainer requires the security token it
	// prints to its logs.
	token := setupToken(ctx, t, p)
	compatibility.Request{
		Method:      http.MethodPost,
		URL:         base + "/api/users/admin/init",
		ContentType: "application/json",
		Headers:     map[string]string{"X-Setup-Token": token},
		Body:        fmt.Sprintf(`{"Username":%q,"Password":%q}`, adminUser, adminPassword),
	}.DoOrFatal(t, http.StatusOK)

	// Log in and capture the JWT.
	authBody := compatibility.Request{
		Method:      http.MethodPost,
		URL:         base + "/api/auth",
		ContentType: "application/json",
		Body:        fmt.Sprintf(`{"Username":%q,"Password":%q}`, adminUser, adminPassword),
	}.DoOrFatal(t, http.StatusOK)
	var auth struct {
		JWT string `json:"jwt"`
	}
	if err := json.Unmarshal([]byte(authBody), &auth); err != nil || auth.JWT == "" {
		t.Fatalf("login: no jwt (%v); body: %s", err, authBody)
	}
	bearer := map[string]string{"Authorization": "Bearer " + auth.JWT}

	// Connect Portainer to the Docker environment through the proxy.
	form := url.Values{
		"Name":                 {"primary"},
		"EndpointCreationType": {"1"}, // Docker environment via URL.
		"URL":                  {fmt.Sprintf("tcp://%s:%d", proxyHost, proxyPort)},
		"TLS":                  {"false"},
	}
	epBody := compatibility.Request{
		Method:      http.MethodPost,
		URL:         base + "/api/endpoints",
		ContentType: "application/x-www-form-urlencoded",
		Headers:     bearer,
		Body:        form.Encode(),
	}.DoOrFatal(t, http.StatusOK)
	var endpoint struct {
		ID int `json:"Id"`
	}
	if err := json.Unmarshal([]byte(epBody), &endpoint); err != nil || endpoint.ID == 0 {
		t.Fatalf("create endpoint: bad response (%v); body: %s", err, epBody)
	}

	// Drive the Docker API through Portainer's endpoint proxy.
	ver := compatibility.Request{
		URL:     fmt.Sprintf("%s/api/endpoints/%d/docker/version", base, endpoint.ID),
		Headers: bearer,
	}.DoOrFatal(t, http.StatusOK)
	if !strings.Contains(ver, "ApiVersion") {
		t.Fatalf("docker version via portainer: unexpected body %s", ver)
	}
	t.Logf("portainer managed Docker through the proxy (endpoint %d)", endpoint.ID)
}

// setupToken reads the Portainer container logs and extracts the admin
// initialization security token.
func setupToken(ctx context.Context, t *testing.T, c *dockerutil.Container) string {
	t.Helper()
	var token string
	compatibility.Poll(ctx, t, "portainer setup token in logs", readyTimeout, pollInterval, func() error {
		logs, err := c.Logs(ctx)
		if err != nil {
			return err
		}
		m := setupTokenRE.FindStringSubmatch(ansiRE.ReplaceAllString(logs, ""))
		if m == nil {
			return fmt.Errorf("token not yet in logs")
		}
		token = m[1]
		return nil
	})
	return token
}

func TestMain(m *testing.M) {
	dockerutil.EnsureSupportedDockerVersion()
	flag.Parse()
	os.Exit(m.Run())
}
