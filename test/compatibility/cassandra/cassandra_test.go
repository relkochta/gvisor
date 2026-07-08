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

// Package cassandra is a gVisor compatibility test for Apache Cassandra.
//
// The Cassandra version under test is pinned in
// images/compatibility/cassandra/cassandra/Dockerfile.
package cassandra

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/test/dockerutil"
	"gvisor.dev/gvisor/test/compatibility"
)

const (
	cassandraImage = "compatibility/cassandra/cassandra"

	want = "gvisor-row"

	// Cassandra's JVM node takes a while to open the CQL port.
	readyTimeout = 5 * time.Minute
	pollInterval = 5 * time.Second
)

func TestCassandra(t *testing.T) {
	ctx := context.Background()

	c := dockerutil.MakeContainer(ctx, t)
	defer c.CleanUp(ctx)
	if err := c.Spawn(ctx, dockerutil.RunOpts{
		Image: cassandraImage,
		Env: []string{
			"MAX_HEAP_SIZE=1024M",
			"HEAP_NEWSIZE=256M",
			"CASSANDRA_CLUSTER_NAME=gvtest",
			"CASSANDRA_NUM_TOKENS=1",
		},
	}); err != nil {
		t.Fatalf("failed to start cassandra: %v", err)
	}

	cql := func(stmt string) (string, error) {
		return c.Exec(ctx, dockerutil.ExecOpts{}, "cqlsh", "-e", stmt)
	}

	// Wait for the CQL interface to answer.
	compatibility.Poll(ctx, t, "cassandra CQL to be ready", readyTimeout, pollInterval, func() error {
		out, err := cql("SELECT release_version FROM system.local")
		if err != nil {
			return fmt.Errorf("cqlsh: %v (%s)", err, out)
		}
		return nil
	})

	// Create a keyspace/table, insert a row, and read it back.
	out, err := cql("CREATE KEYSPACE gv WITH replication = {'class':'SimpleStrategy','replication_factor':1}; " +
		"CREATE TABLE gv.t (id int PRIMARY KEY, v text); " +
		"INSERT INTO gv.t (id, v) VALUES (1, '" + want + "'); " +
		"SELECT v FROM gv.t WHERE id = 1;")
	if err != nil {
		t.Fatalf("cassandra roundtrip failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, want) {
		t.Fatalf("cassandra roundtrip: output missing %q; got: %s", want, out)
	}
	t.Logf("cassandra roundtrip ok")
}

func TestMain(m *testing.M) {
	dockerutil.EnsureSupportedDockerVersion()
	flag.Parse()
	os.Exit(m.Run())
}
