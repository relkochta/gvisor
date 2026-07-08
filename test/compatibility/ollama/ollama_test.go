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

// Package ollama is a gVisor compatibility test for Ollama (CPU inference).
//
// The version under test is pinned in
// images/compatibility/ollama/ollama/Dockerfile.
package ollama

import (
	"context"
	"encoding/json"
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
	ollamaImage = "compatibility/ollama/ollama"
	model       = "smollm:135m" // baked into the image; see the Dockerfile.

	ollamaPort = 11434

	readyTimeout    = 3 * time.Minute
	pollInterval    = 2 * time.Second
	generateTimeout = 4 * time.Minute // Allow lots of time for CPU inference.
)

func TestOllama(t *testing.T) {
	ctx := context.Background()

	c := dockerutil.MakeContainer(ctx, t)
	defer c.CleanUp(ctx)
	if err := c.Spawn(ctx, dockerutil.RunOpts{
		Image: ollamaImage,
		Env:   []string{"OLLAMA_HOST=0.0.0.0"},
	}); err != nil {
		t.Fatalf("failed to start ollama: %v", err)
	}

	ip, err := c.FindIP(ctx, false)
	if err != nil {
		t.Fatalf("failed to find ollama IP: %v", err)
	}
	base := fmt.Sprintf("http://%s:%d", ip.String(), ollamaPort)

	// Wait for the server to be up.
	compatibility.Poll(ctx, t, "ollama server to be ready", readyTimeout, pollInterval, func() error {
		status, body, err := compatibility.Get(base + "/api/version")
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("GET /api/version: status %d", status)
		}
		if !strings.Contains(body, "version") {
			return fmt.Errorf("unexpected /api/version body: %s", body)
		}
		return nil
	})

	// Confirm the baked-in model is present.
	tags := compatibility.Request{URL: base + "/api/tags"}.DoOrFatal(t, http.StatusOK)
	if !strings.Contains(tags, model) {
		t.Fatalf("model %q not found in /api/tags: %s", model, tags)
	}

	// Run a CPU inference.
	body := compatibility.Request{
		Method:      http.MethodPost,
		URL:         base + "/api/generate",
		ContentType: "application/json",
		Body:        fmt.Sprintf(`{"model":%q,"prompt":"Reply with exactly one word: hello","stream":false}`, model),
		Timeout:     generateTimeout,
	}.DoOrFatal(t, http.StatusOK)

	var gen struct {
		Response  string `json:"response"`
		Done      bool   `json:"done"`
		EvalCount int    `json:"eval_count"`
	}
	if err := json.Unmarshal([]byte(body), &gen); err != nil {
		t.Fatalf("generate: bad JSON (%v): %s", err, body)
	}
	if !gen.Done || gen.EvalCount <= 0 {
		t.Fatalf("generate: no tokens produced (done=%v eval_count=%d): %s", gen.Done, gen.EvalCount, body)
	}
	t.Logf("ollama generated %d tokens; response=%q", gen.EvalCount, strings.TrimSpace(gen.Response))
}

func TestMain(m *testing.M) {
	dockerutil.EnsureSupportedDockerVersion()
	flag.Parse()
	os.Exit(m.Run())
}
