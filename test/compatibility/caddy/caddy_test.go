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

// Package caddy is a gVisor compatibility test for the Caddy web server.
//
// The Caddy version under test is pinned in
// images/compatibility/caddy/caddy/Dockerfile.
package caddy

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
	caddyImage = "compatibility/caddy/caddy"
	echoImage  = "compatibility/caddy/echo"
	caddyPort  = 80

	echoText = "hello-from-echo"
	selfText = "hello-from-caddy"

	readyTimeout = 1 * time.Minute
	pollInterval = 2 * time.Second
)

const caddyfile = `:80 {
	handle /echo* {
		reverse_proxy echo:5678
	}
	handle {
		respond "hello-from-caddy" 200
	}
}
`

func TestCaddy(t *testing.T) {
	ctx := context.Background()

	// Backend.
	echo := dockerutil.MakeContainer(ctx, t)
	defer echo.CleanUp(ctx)
	if err := echo.Spawn(ctx, dockerutil.RunOpts{Image: echoImage}, "-text="+echoText, "-listen=:5678"); err != nil {
		t.Fatalf("failed to start echo backend: %v", err)
	}

	// Caddy, linked to the backend as "echo", with the inline Caddyfile mounted.
	c := dockerutil.MakeContainer(ctx, t)
	defer c.CleanUp(ctx)
	opts := dockerutil.RunOpts{
		Image: caddyImage,
		Links: []string{echo.MakeLink("echo")},
	}
	c.CopyFiles(&opts, "/etc/caddy", compatibility.WriteConfigFile(t, "Caddyfile", caddyfile))
	if err := c.Spawn(ctx, opts); err != nil {
		t.Fatalf("failed to start caddy: %v", err)
	}

	ip, err := c.FindIP(ctx, false)
	if err != nil {
		t.Fatalf("failed to find caddy IP: %v", err)
	}
	base := fmt.Sprintf("http://%s:%d", ip.String(), caddyPort)

	// Caddy answering directly.
	compatibility.Poll(ctx, t, "caddy to serve", readyTimeout, pollInterval, func() error {
		status, body, err := compatibility.Get(base + "/")
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("GET /: status %d", status)
		}
		if !strings.Contains(body, selfText) {
			return fmt.Errorf("unexpected body: %s", body)
		}
		return nil
	})

	// Caddy reverse-proxying to the backend.
	got := compatibility.Request{URL: base + "/echo"}.DoOrFatal(t, http.StatusOK)
	if !strings.Contains(got, echoText) {
		t.Fatalf("proxied response: got %q, want to contain %q", strings.TrimSpace(got), echoText)
	}
	t.Logf("caddy served directly and reverse-proxied to the backend")
}

func TestMain(m *testing.M) {
	dockerutil.EnsureSupportedDockerVersion()
	flag.Parse()
	os.Exit(m.Run())
}
