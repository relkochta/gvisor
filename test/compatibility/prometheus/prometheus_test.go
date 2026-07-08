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

// Package prometheus is a gVisor compatibility test for Prometheus.
//
// The Prometheus version under test is pinned in
// images/compatibility/prometheus/prometheus/Dockerfile.
package prometheus

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/test/dockerutil"
	"gvisor.dev/gvisor/test/compatibility"
)

const (
	prometheusImage = "compatibility/prometheus/prometheus"
	prometheusPort  = 9090

	readyTimeout = 2 * time.Minute
	pollInterval = 2 * time.Second
)

const prometheusConfig = `global:
  scrape_interval: 5s
  evaluation_interval: 5s

scrape_configs:
  - job_name: prometheus
    static_configs:
      - targets: ["localhost:9090"]
`

func TestPrometheus(t *testing.T) {
	ctx := context.Background()

	c := dockerutil.MakeContainer(ctx, t)
	defer c.CleanUp(ctx)
	opts := dockerutil.RunOpts{Image: prometheusImage}
	c.CopyFiles(&opts, "/etc/prometheus", compatibility.WriteConfigFile(t, "prometheus.yml", prometheusConfig))
	if err := c.Spawn(ctx, opts); err != nil {
		t.Fatalf("failed to start prometheus: %v", err)
	}

	ip, err := c.FindIP(ctx, false)
	if err != nil {
		t.Fatalf("failed to find prometheus IP: %v", err)
	}
	base := fmt.Sprintf("http://%s:%d", ip.String(), prometheusPort)

	// Wait for Prometheus to be ready.
	compatibility.Poll(ctx, t, "prometheus to be ready", readyTimeout, pollInterval, func() error {
		status, _, err := compatibility.Get(base + "/-/ready")
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("GET /-/ready: status %d", status)
		}
		return nil
	})

	// Wait for the self-scrape target to report up=1, then confirm the query API
	// returns it.
	compatibility.Poll(ctx, t, "prometheus self-scrape to report up", readyTimeout, pollInterval, func() error {
		status, body, err := compatibility.Get(base + "/api/v1/query?query=up")
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("query up: status %d", status)
		}
		var q struct {
			Status string `json:"status"`
			Data   struct {
				Result []struct {
					Value [2]interface{} `json:"value"`
				} `json:"result"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(body), &q); err != nil {
			return fmt.Errorf("query up: bad JSON: %v", err)
		}
		if q.Status != "success" || len(q.Data.Result) == 0 {
			return fmt.Errorf("query up: no results yet: %s", body)
		}
		if v, ok := q.Data.Result[0].Value[1].(string); !ok || v != "1" {
			return fmt.Errorf("up != 1 yet (got %v)", q.Data.Result[0].Value[1])
		}
		return nil
	})
	t.Logf("prometheus scraped itself and served up=1 via the query API")
}

func TestMain(m *testing.M) {
	dockerutil.EnsureSupportedDockerVersion()
	flag.Parse()
	os.Exit(m.Run())
}
