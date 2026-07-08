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

// Package memcached is a gVisor compatibility test for Memcached.
//
// The Memcached version under test is pinned in
// images/compatibility/memcached/memcached/Dockerfile.
package memcached

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/test/dockerutil"
	"gvisor.dev/gvisor/test/compatibility"
)

const (
	memcachedImage = "compatibility/memcached/memcached"
	memcachedPort  = 11211

	key   = "gv-key"
	value = "gvisor-value"

	readyTimeout = 2 * time.Minute
	pollInterval = 2 * time.Second
	ioTimeout    = 10 * time.Second
)

func TestMemcached(t *testing.T) {
	ctx := context.Background()

	c := dockerutil.MakeContainer(ctx, t)
	defer c.CleanUp(ctx)
	if err := c.Spawn(ctx, dockerutil.RunOpts{Image: memcachedImage}); err != nil {
		t.Fatalf("failed to start memcached: %v", err)
	}

	ip, err := c.FindIP(ctx, false)
	if err != nil {
		t.Fatalf("failed to find memcached IP: %v", err)
	}
	addr := fmt.Sprintf("%s:%d", ip.String(), memcachedPort)

	// Wait for the server to answer the "version" command.
	compatibility.Poll(ctx, t, "memcached to respond", readyTimeout, pollInterval, func() error {
		out, err := command(addr, "version\r\n", "\r\n")
		if err != nil {
			return err
		}
		if !strings.HasPrefix(out, "VERSION") {
			return fmt.Errorf("unexpected version reply: %q", strings.TrimSpace(out))
		}
		return nil
	})

	// Store a key.
	set := fmt.Sprintf("set %s 0 0 %d\r\n%s\r\n", key, len(value), value)
	if out, err := command(addr, set, "\r\n"); err != nil {
		t.Fatalf("set failed: %v", err)
	} else if !strings.HasPrefix(out, "STORED") {
		t.Fatalf("set: got %q, want STORED", strings.TrimSpace(out))
	}

	// Read it back.
	out, err := command(addr, fmt.Sprintf("get %s\r\n", key), "END\r\n")
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if !strings.Contains(out, value) {
		t.Fatalf("get %s: response missing %q; got: %q", key, value, out)
	}
	t.Logf("memcached roundtrip ok")
}

// command opens a connection, sends req, and reads the reply until it contains
// until (or the deadline elapses).
func command(addr, req, until string) (string, error) {
	conn, err := net.DialTimeout("tcp", addr, ioTimeout)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(ioTimeout)); err != nil {
		return "", err
	}
	if _, err := conn.Write([]byte(req)); err != nil {
		return "", err
	}
	var buf bytes.Buffer
	tmp := make([]byte, 1024)
	for {
		n, err := conn.Read(tmp)
		if n > 0 {
			buf.Write(tmp[:n])
			if strings.Contains(buf.String(), until) {
				return buf.String(), nil
			}
		}
		if err != nil {
			return buf.String(), err
		}
	}
}

func TestMain(m *testing.M) {
	dockerutil.EnsureSupportedDockerVersion()
	flag.Parse()
	os.Exit(m.Run())
}
