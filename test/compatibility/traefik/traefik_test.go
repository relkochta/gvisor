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

// Package traefik is a gVisor compatibility test for the Traefik proxy.
//
// The Traefik version under test is pinned in
// images/compatibility/traefik/traefik/Dockerfile.
package traefik

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
	traefikImage = "compatibility/traefik/traefik"
	echoImage    = "compatibility/traefik/echo"
	webPort      = 80

	echoText = "hello-from-echo"

	readyTimeout = 1 * time.Minute
	pollInterval = 2 * time.Second
)

const dynamicYML = `http:
  routers:
    echo:
      rule: "PathPrefix(` + "`" + `/` + "`" + `)"
      entryPoints: ["web"]
      service: echo
  services:
    echo:
      loadBalancer:
        servers:
          - url: "http://echo:5678"
`

func TestTraefik(t *testing.T) {
	ctx := context.Background()

	// Backend.
	echo := dockerutil.MakeContainer(ctx, t)
	defer echo.CleanUp(ctx)
	if err := echo.Spawn(ctx, dockerutil.RunOpts{Image: echoImage}, "-text="+echoText, "-listen=:5678"); err != nil {
		t.Fatalf("failed to start echo backend: %v", err)
	}

	// Traefik with the inline file-provider config, linked to the backend.
	c := dockerutil.MakeContainer(ctx, t)
	defer c.CleanUp(ctx)
	opts := dockerutil.RunOpts{
		Image: traefikImage,
		Links: []string{echo.MakeLink("echo")},
	}
	c.CopyFiles(&opts, "/etc/traefik", compatibility.WriteConfigFile(t, "dynamic.yml", dynamicYML))
	if err := c.Spawn(ctx, opts,
		"--entrypoints.web.address=:80",
		"--providers.file.filename=/etc/traefik/dynamic.yml",
	); err != nil {
		t.Fatalf("failed to start traefik: %v", err)
	}

	ip, err := c.FindIP(ctx, false)
	if err != nil {
		t.Fatalf("failed to find traefik IP: %v", err)
	}
	base := fmt.Sprintf("http://%s:%d", ip.String(), webPort)

	// Traefik should route requests to the backend once the file provider loads.
	compatibility.Poll(ctx, t, "traefik to route to the backend", readyTimeout, pollInterval, func() error {
		status, body, err := compatibility.Get(base + "/")
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("GET /: status %d", status)
		}
		if !strings.Contains(body, echoText) {
			return fmt.Errorf("unexpected body: %s", strings.TrimSpace(body))
		}
		return nil
	})
	t.Logf("traefik routed and proxied to the backend")
}

func TestMain(m *testing.M) {
	dockerutil.EnsureSupportedDockerVersion()
	flag.Parse()
	os.Exit(m.Run())
}
