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

// Package haproxy is a gVisor compatibility test for HAProxy.
//
// The HAProxy version under test is pinned in
// images/compatibility/haproxy/haproxy/Dockerfile.
package haproxy

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
	haproxyImage = "compatibility/haproxy/haproxy"
	echoImage    = "compatibility/haproxy/echo"
	haproxyPort  = 80

	echoText = "hello-from-echo"

	readyTimeout = 1 * time.Minute
	pollInterval = 2 * time.Second
)

const haproxyCfg = `global
    daemon
    maxconn 2000

defaults
    mode http
    timeout connect 5s
    timeout client 30s
    timeout server 30s

frontend fe
    bind :80
    default_backend be

backend be
    server s1 echo:5678 check
`

func TestHAProxy(t *testing.T) {
	ctx := context.Background()

	// Backend.
	echo := dockerutil.MakeContainer(ctx, t)
	defer echo.CleanUp(ctx)
	if err := echo.Spawn(ctx, dockerutil.RunOpts{Image: echoImage}, "-text="+echoText, "-listen=:5678"); err != nil {
		t.Fatalf("failed to start echo backend: %v", err)
	}

	// HAProxy, linked to the backend as "echo".
	c := dockerutil.MakeContainer(ctx, t)
	defer c.CleanUp(ctx)
	opts := dockerutil.RunOpts{
		Image: haproxyImage,
		Links: []string{echo.MakeLink("echo")},
	}
	c.CopyFiles(&opts, "/usr/local/etc/haproxy", compatibility.WriteConfigFile(t, "haproxy.cfg", haproxyCfg))
	if err := c.Spawn(ctx, opts); err != nil {
		t.Fatalf("failed to start haproxy: %v", err)
	}

	ip, err := c.FindIP(ctx, false)
	if err != nil {
		t.Fatalf("failed to find haproxy IP: %v", err)
	}
	base := fmt.Sprintf("http://%s:%d", ip.String(), haproxyPort)

	// HAProxy should proxy to the backend.
	compatibility.Poll(ctx, t, "haproxy to proxy to the backend", readyTimeout, pollInterval, func() error {
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
	t.Logf("haproxy proxied to the backend")
}

func TestMain(m *testing.M) {
	dockerutil.EnsureSupportedDockerVersion()
	flag.Parse()
	os.Exit(m.Run())
}
