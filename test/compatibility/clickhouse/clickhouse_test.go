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

// Package clickhouse is a gVisor compatibility test for ClickHouse.
//
// The ClickHouse version under test is pinned in
// images/compatibility/clickhouse/clickhouse/Dockerfile.
package clickhouse

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
	clickhouseImage = "compatibility/clickhouse/clickhouse"

	chUser     = "default"
	chPassword = "chpass123"
	httpPort   = 8123

	want = "gvisor-row"

	readyTimeout = 2 * time.Minute
	pollInterval = 2 * time.Second
)

// Skip the binary checksum check which fails under gVisor.
const clickhouseConfig = `<clickhouse>
    <skip_binary_checksum_checks>true</skip_binary_checksum_checks>
    <listen_host>::</listen_host>
    <listen_host>0.0.0.0</listen_host>
    <listen_try>1</listen_try>
</clickhouse>
`

func TestClickHouse(t *testing.T) {
	ctx := context.Background()

	c := dockerutil.MakeContainer(ctx, t)
	defer c.CleanUp(ctx)
	opts := dockerutil.RunOpts{
		Image: clickhouseImage,
		Env: []string{
			"CLICKHOUSE_USER=" + chUser,
			"CLICKHOUSE_PASSWORD=" + chPassword,
			"CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT=1",
		},
	}
	c.CopyFiles(&opts, "/etc/clickhouse-server/config.d", compatibility.WriteConfigFile(t, "gvisor.xml", clickhouseConfig))
	if err := c.Spawn(ctx, opts); err != nil {
		t.Fatalf("failed to start clickhouse: %v", err)
	}

	ip, err := c.FindIP(ctx, false)
	if err != nil {
		t.Fatalf("failed to find clickhouse IP: %v", err)
	}
	base := fmt.Sprintf("http://%s:%d", ip.String(), httpPort)

	// Wait for the server to be ready.
	compatibility.Poll(ctx, t, "clickhouse to answer /ping", readyTimeout, pollInterval, func() error {
		status, body, err := compatibility.Get(base + "/ping")
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("GET /ping: status %d", status)
		}
		if !strings.Contains(body, "Ok") {
			return fmt.Errorf("GET /ping: body %q", strings.TrimSpace(body))
		}
		return nil
	})

	// query runs a SQL statement over the HTTP interface and returns the body.
	query := func(sql string) string {
		return compatibility.Request{
			Method:   http.MethodPost,
			URL:      base + "/",
			Body:     sql,
			Username: chUser,
			Password: chPassword,
		}.DoOrFatal(t, http.StatusOK)
	}

	t.Logf("clickhouse version: %s", strings.TrimSpace(query("SELECT version()")))

	// Create a table, insert a row, and read it back.
	query("CREATE TABLE gv (id UInt64, v String) ENGINE = MergeTree ORDER BY id")
	query("INSERT INTO gv VALUES (1, '" + want + "')")
	got := query("SELECT v FROM gv WHERE id = 1")
	if !strings.Contains(got, want) {
		t.Fatalf("clickhouse roundtrip: output missing %q; got: %q", want, strings.TrimSpace(got))
	}
	t.Logf("clickhouse roundtrip ok")
}

func TestMain(m *testing.M) {
	dockerutil.EnsureSupportedDockerVersion()
	flag.Parse()
	os.Exit(m.Run())
}
