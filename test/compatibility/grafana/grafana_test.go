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

// Package grafana is a gVisor compatibility test for Grafana.
//
// The Grafana version under test is pinned in
// images/compatibility/grafana/grafana/Dockerfile.
package grafana

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
	grafanaImage = "compatibility/grafana/grafana"
	grafanaPort  = 3000

	adminUser     = "admin"
	adminPassword = "gvisorpass"

	folderUID   = "gvfolder"
	folderTitle = "gVisor Test Folder"

	readyTimeout = 2 * time.Minute
	pollInterval = 2 * time.Second
)

func TestGrafana(t *testing.T) {
	ctx := context.Background()

	c := dockerutil.MakeContainer(ctx, t)
	defer c.CleanUp(ctx)
	if err := c.Spawn(ctx, dockerutil.RunOpts{
		Image: grafanaImage,
		Env: []string{
			"GF_SECURITY_ADMIN_USER=" + adminUser,
			"GF_SECURITY_ADMIN_PASSWORD=" + adminPassword,
		},
	}); err != nil {
		t.Fatalf("failed to start grafana: %v", err)
	}

	ip, err := c.FindIP(ctx, false)
	if err != nil {
		t.Fatalf("failed to find grafana IP: %v", err)
	}
	base := fmt.Sprintf("http://%s:%d", ip.String(), grafanaPort)

	// Wait for Grafana to report a healthy database.
	compatibility.Poll(ctx, t, "grafana to be healthy", readyTimeout, pollInterval, func() error {
		status, body, err := compatibility.Get(base + "/api/health")
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("GET /api/health: status %d", status)
		}
		if !strings.Contains(body, `"database": "ok"`) {
			return fmt.Errorf("database not ok yet: %s", strings.TrimSpace(body))
		}
		return nil
	})

	// Create a folder (a DB write).
	compatibility.Request{
		Method:      http.MethodPost,
		URL:         base + "/api/folders",
		ContentType: "application/json",
		Username:    adminUser,
		Password:    adminPassword,
		Body:        fmt.Sprintf(`{"uid":%q,"title":%q}`, folderUID, folderTitle),
	}.DoOrFatal(t, http.StatusOK)

	// Read it back (a DB read).
	got := compatibility.Request{
		URL:      base + "/api/folders/" + folderUID,
		Username: adminUser,
		Password: adminPassword,
	}.DoOrFatal(t, http.StatusOK)
	if !strings.Contains(got, folderTitle) {
		t.Fatalf("read folder back: missing %q; body: %s", folderTitle, got)
	}
	t.Logf("grafana folder create/read roundtrip ok")
}

func TestMain(m *testing.M) {
	dockerutil.EnsureSupportedDockerVersion()
	flag.Parse()
	os.Exit(m.Run())
}
