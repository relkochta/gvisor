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

// Package mongodb is a gVisor compatibility test for MongoDB.
//
// The MongoDB version under test is pinned in
// images/compatibility/mongodb/mongodb/Dockerfile.
package mongodb

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
	mongoImage = "compatibility/mongodb/mongodb"

	want = "gvisor-doc"

	readyTimeout = 2 * time.Minute
	pollInterval = 2 * time.Second
)

func TestMongoDB(t *testing.T) {
	ctx := context.Background()

	c := dockerutil.MakeContainer(ctx, t)
	defer c.CleanUp(ctx)
	if err := c.Spawn(ctx, dockerutil.RunOpts{Image: mongoImage}); err != nil {
		t.Fatalf("failed to start mongodb: %v", err)
	}

	eval := func(js string) (string, error) {
		return c.Exec(ctx, dockerutil.ExecOpts{}, "mongosh", "--quiet", "--eval", js)
	}

	// Wait for the server to answer a ping.
	compatibility.Poll(ctx, t, "mongodb to answer ping", readyTimeout, pollInterval, func() error {
		out, err := eval("db.runCommand({ping: 1}).ok")
		if err != nil {
			return fmt.Errorf("ping: %v (%s)", err, out)
		}
		if !strings.Contains(out, "1") {
			return fmt.Errorf("ping not ok: %q", strings.TrimSpace(out))
		}
		return nil
	})

	// Insert a document and read it back.
	out, err := eval(`db.gv.insertOne({_id: 1, v: "` + want + `"}); print(db.gv.findOne({_id: 1}).v)`)
	if err != nil {
		t.Fatalf("mongodb roundtrip failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, want) {
		t.Fatalf("mongodb roundtrip: output missing %q; got: %s", want, out)
	}
	t.Logf("mongodb roundtrip ok")
}

func TestMain(m *testing.M) {
	dockerutil.EnsureSupportedDockerVersion()
	flag.Parse()
	os.Exit(m.Run())
}
