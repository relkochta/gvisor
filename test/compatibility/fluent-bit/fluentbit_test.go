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

// Package fluentbit is a gVisor compatibility test for Fluent Bit.
//
// The Fluent Bit version under test is pinned in
// images/compatibility/fluent-bit/fluent-bit/Dockerfile.
package fluentbit

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
	fluentbitImage = "compatibility/fluent-bit/fluent-bit"
	httpPort       = 2020

	marker = "gvisor-fluent-test"

	readyTimeout = 1 * time.Minute
	pollInterval = 2 * time.Second
)

const fluentbitConfig = `[SERVICE]
    Flush        1
    HTTP_Server  On
    HTTP_Listen  0.0.0.0
    HTTP_Port    2020

[INPUT]
    Name   dummy
    Dummy  {"message":"gvisor-fluent-test"}
    Tag    test
    Rate   1

[OUTPUT]
    Name   stdout
    Match  *
`

func TestFluentBit(t *testing.T) {
	ctx := context.Background()

	c := dockerutil.MakeContainer(ctx, t)
	defer c.CleanUp(ctx)
	opts := dockerutil.RunOpts{Image: fluentbitImage}
	c.CopyFiles(&opts, "/etc/fluentbit", compatibility.WriteConfigFile(t, "fluent-bit.conf", fluentbitConfig))
	if err := c.Spawn(ctx, opts, "-c", "/etc/fluentbit/fluent-bit.conf"); err != nil {
		t.Fatalf("failed to start fluent-bit: %v", err)
	}

	ip, err := c.FindIP(ctx, false)
	if err != nil {
		t.Fatalf("failed to find fluent-bit IP: %v", err)
	}

	// Wait for the HTTP monitoring server.
	compatibility.Poll(ctx, t, "fluent-bit HTTP server to be ready", readyTimeout, pollInterval, func() error {
		status, _, err := compatibility.Get(fmt.Sprintf("http://%s:%d/api/v1/uptime", ip.String(), httpPort))
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("GET /api/v1/uptime: status %d", status)
		}
		return nil
	})

	// The dummy input should flow to the container log.
	compatibility.Poll(ctx, t, "fluent-bit to emit dummy records", readyTimeout, pollInterval, func() error {
		logs, err := c.Logs(ctx)
		if err != nil {
			return err
		}
		if !strings.Contains(logs, marker) {
			return fmt.Errorf("dummy records not in stdout output yet")
		}
		return nil
	})
	t.Logf("fluent-bit processed records through input -> output")
}

func TestMain(m *testing.M) {
	dockerutil.EnsureSupportedDockerVersion()
	flag.Parse()
	os.Exit(m.Run())
}
