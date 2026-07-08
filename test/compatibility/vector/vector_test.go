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

// Package vector is a gVisor compatibility test for Vector.
//
// The Vector version under test is pinned in
// images/compatibility/vector/vector/Dockerfile.
package vector

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
	vectorImage = "compatibility/vector/vector"

	apiPort    = 8686 // Vector API (/health).
	sourcePort = 8180 // http_server source.

	eventMarker = "gvisor-vector-event"
	tagMarker   = "vector-gvisor-test" // added by the remap transform.

	readyTimeout = 2 * time.Minute
	pollInterval = 2 * time.Second
)

const vectorConfig = `api:
  enabled: true
  address: 0.0.0.0:8686

sources:
  http_in:
    type: http_server
    address: 0.0.0.0:8180
    decoding:
      codec: json
  demo:
    type: demo_logs
    format: shuffle
    lines: ["gvisor-demo-line"]
    interval: 1.0

transforms:
  tag:
    type: remap
    inputs: [http_in]
    source: |
      .processed_by = "vector-gvisor-test"

sinks:
  out:
    type: console
    inputs: [tag, demo]
    encoding:
      codec: json
`

func TestVector(t *testing.T) {
	ctx := context.Background()

	c := dockerutil.MakeContainer(ctx, t)
	defer c.CleanUp(ctx)
	opts := dockerutil.RunOpts{
		Image: vectorImage,
		Env:   []string{"VECTOR_LOG=info"},
	}
	c.CopyFiles(&opts, "/etc/vector", compatibility.WriteConfigFile(t, "vector.yaml", vectorConfig))
	if err := c.Spawn(ctx, opts, "--config", "/etc/vector/vector.yaml"); err != nil {
		t.Fatalf("failed to start vector: %v", err)
	}

	ip, err := c.FindIP(ctx, false)
	if err != nil {
		t.Fatalf("failed to find vector IP: %v", err)
	}

	// Wait for the Vector API to be healthy.
	compatibility.Poll(ctx, t, "vector API to be healthy", readyTimeout, pollInterval, func() error {
		status, _, err := compatibility.Get(fmt.Sprintf("http://%s:%d/health", ip.String(), apiPort))
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("GET /health: status %d", status)
		}
		return nil
	})

	// Push an event into the http_server source.
	compatibility.Request{
		Method:      http.MethodPost,
		URL:         fmt.Sprintf("http://%s:%d/", ip.String(), sourcePort),
		ContentType: "application/json",
		Body:        fmt.Sprintf(`{"message":%q}`, eventMarker),
	}.DoOrFatal(t, http.StatusOK)

	// The event should flow source -> remap -> console sink and appear in the
	// container logs, tagged by the transform.
	compatibility.Poll(ctx, t, "vector to emit the processed event", readyTimeout, pollInterval, func() error {
		logs, err := c.Logs(ctx)
		if err != nil {
			return err
		}
		if !strings.Contains(logs, eventMarker) || !strings.Contains(logs, tagMarker) {
			return fmt.Errorf("processed event not in console sink output yet")
		}
		return nil
	})
	t.Logf("vector processed an event through source -> transform -> sink")
}

func TestMain(m *testing.M) {
	dockerutil.EnsureSupportedDockerVersion()
	flag.Parse()
	os.Exit(m.Run())
}
