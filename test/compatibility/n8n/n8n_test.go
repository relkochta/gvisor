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

// Package n8n is a gVisor compatibility test for n8n backed by PostgreSQL.
//
// The version under test is pinned in images/compatibility/n8n/n8n/Dockerfile.
package n8n

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
	n8nImage      = "compatibility/n8n/n8n"
	postgresImage = "compatibility/n8n/postgres"

	dbName     = "n8n"
	dbUser     = "n8n"
	dbPassword = "n8npass"

	ownerEmail     = "admin@example.com"
	ownerFirstName = "gVisor"
	ownerLastName  = "Admin"
	ownerPassword  = "Gvisor-Test-1234"

	n8nPort = 5678

	readyTimeout = 3 * time.Minute
	pollInterval = 2 * time.Second
)

func TestN8n(t *testing.T) {
	ctx := context.Background()

	// PostgreSQL backend.
	db := dockerutil.MakeContainer(ctx, t)
	defer db.CleanUp(ctx)
	if err := db.Spawn(ctx, dockerutil.RunOpts{
		Image: postgresImage,
		Env: []string{
			"POSTGRES_DB=" + dbName,
			"POSTGRES_USER=" + dbUser,
			"POSTGRES_PASSWORD=" + dbPassword,
		},
	}); err != nil {
		t.Fatalf("failed to start postgres: %v", err)
	}

	// Wait until Postgres accepts connections before starting n8n.
	compatibility.Poll(ctx, t, "postgres to accept connections", readyTimeout, pollInterval, func() error {
		out, err := db.Exec(ctx, dockerutil.ExecOpts{}, "pg_isready", "-U", dbUser, "-d", dbName)
		if err != nil {
			return fmt.Errorf("pg_isready: %v (%s)", err, out)
		}
		return nil
	})

	// n8n app container.
	app := dockerutil.MakeContainer(ctx, t)
	defer app.CleanUp(ctx)
	if err := app.Spawn(ctx, dockerutil.RunOpts{
		Image: n8nImage,
		Links: []string{db.MakeLink("db")},
		Env: []string{
			"DB_TYPE=postgresdb",
			"DB_POSTGRESDB_HOST=db",
			"DB_POSTGRESDB_PORT=5432",
			"DB_POSTGRESDB_DATABASE=" + dbName,
			"DB_POSTGRESDB_USER=" + dbUser,
			"DB_POSTGRESDB_PASSWORD=" + dbPassword,
			"N8N_DIAGNOSTICS_ENABLED=false", // no external telemetry
		},
	}); err != nil {
		t.Fatalf("failed to start n8n: %v", err)
	}

	ip, err := app.FindIP(ctx, false)
	if err != nil {
		t.Fatalf("failed to find n8n IP: %v", err)
	}
	base := fmt.Sprintf("http://%s:%d", ip.String(), n8nPort)

	// Wait until n8n's REST API is fully up (determined by /rest/settings returning
	// settingsMode).
	compatibility.Poll(ctx, t, "n8n REST API to be ready", readyTimeout, pollInterval, func() error {
		status, body, err := compatibility.Get(base + "/rest/settings")
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("GET /rest/settings: status %d", status)
		}
		if !strings.Contains(body, `"settingsMode"`) {
			return fmt.Errorf("n8n REST API not ready yet (got non-JSON response)")
		}
		return nil
	})

	// Complete the initial owner setup via the REST API.
	setupBody := fmt.Sprintf(
		`{"email":%q,"firstName":%q,"lastName":%q,"password":%q}`,
		ownerEmail, ownerFirstName, ownerLastName, ownerPassword)
	respBody := compatibility.Request{
		Method:      http.MethodPost,
		URL:         base + "/rest/owner/setup",
		ContentType: "application/json",
		Body:        setupBody,
	}.DoOrFatal(t, http.StatusOK)
	if !strings.Contains(respBody, ownerEmail) {
		t.Fatalf("owner setup: response missing %q; got: %s", ownerEmail, respBody)
	}
	if !strings.Contains(respBody, "owner") {
		t.Fatalf("owner setup: response missing role %q; got: %s", "owner", respBody)
	}
}

func TestMain(m *testing.M) {
	dockerutil.EnsureSupportedDockerVersion()
	flag.Parse()
	os.Exit(m.Run())
}
