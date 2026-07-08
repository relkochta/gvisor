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

// Package envoy is a gVisor compatibility test for the Envoy proxy.
//
// The Envoy version under test is pinned in
// images/compatibility/envoy/envoy/Dockerfile.
package envoy

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/test/dockerutil"
	"gvisor.dev/gvisor/test/compatibility"
)

const (
	envoyImage = "compatibility/envoy/envoy"
	echoImage  = "compatibility/envoy/echo"

	listenerPort = 10000
	adminPort    = 9901

	echoText = "hello-from-echo"

	readyTimeout = 1 * time.Minute
	pollInterval = 2 * time.Second
)

const envoyYAML = `admin:
  address:
    socket_address: { address: 0.0.0.0, port_value: 9901 }
static_resources:
  listeners:
  - name: listener_0
    address:
      socket_address: { address: 0.0.0.0, port_value: 10000 }
    filter_chains:
    - filters:
      - name: envoy.filters.network.http_connection_manager
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
          stat_prefix: ingress_http
          route_config:
            name: local_route
            virtual_hosts:
            - name: backend
              domains: ["*"]
              routes:
              - match: { prefix: "/" }
                route: { cluster: echo }
          http_filters:
          - name: envoy.filters.http.router
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router
  clusters:
  - name: echo
    type: STRICT_DNS
    lb_policy: ROUND_ROBIN
    load_assignment:
      cluster_name: echo
      endpoints:
      - lb_endpoints:
        - endpoint:
            address:
              socket_address: { address: echo, port_value: 5678 }
`

func TestEnvoy(t *testing.T) {
	ctx := context.Background()

	// Backend.
	echo := dockerutil.MakeContainer(ctx, t)
	defer echo.CleanUp(ctx)
	if err := echo.Spawn(ctx, dockerutil.RunOpts{Image: echoImage}, "-text="+echoText, "-listen=:5678"); err != nil {
		t.Fatalf("failed to start echo backend: %v", err)
	}

	// Envoy, linked to the backend as "echo".
	c := dockerutil.MakeContainer(ctx, t)
	defer c.CleanUp(ctx)
	opts := dockerutil.RunOpts{
		Image: envoyImage,
		Links: []string{echo.MakeLink("echo")},
	}
	c.CopyFiles(&opts, "/etc/envoy", compatibility.WriteConfigFile(t, "envoy.yaml", envoyYAML))
	if err := c.Spawn(ctx, opts, "-c", "/etc/envoy/envoy.yaml"); err != nil {
		t.Fatalf("failed to start envoy: %v", err)
	}

	ip, err := c.FindIP(ctx, false)
	if err != nil {
		t.Fatalf("failed to find envoy IP: %v", err)
	}

	// Wait for Envoy to report ready via its admin interface.
	compatibility.Poll(ctx, t, "envoy to be ready", readyTimeout, pollInterval, func() error {
		status, body, err := compatibility.Get(fmt.Sprintf("http://%s:%d/ready", ip.String(), adminPort))
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("GET /ready: status %d (%s)", status, strings.TrimSpace(body))
		}
		return nil
	})

	// Envoy should proxy the request to the backend.
	got := compatibility.Request{URL: fmt.Sprintf("http://%s:%d/", ip.String(), listenerPort)}.DoOrFatal(t, http.StatusOK)
	if !strings.Contains(got, echoText) {
		t.Fatalf("proxied response: got %q, want to contain %q", strings.TrimSpace(got), echoText)
	}
	t.Logf("envoy proxied to the backend")
}

func TestMain(m *testing.M) {
	dockerutil.EnsureSupportedDockerVersion()
	flag.Parse()
	os.Exit(m.Run())
}
