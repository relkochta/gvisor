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

// Package minio is a gVisor compatibility test for MinIO (S3 object storage).
//
// The version under test is pinned in
// images/compatibility/minio/{minio,mc}/Dockerfile.
package minio

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
	minioImage = "compatibility/minio/minio"
	mcImage    = "compatibility/minio/mc"

	rootUser     = "minioadmin"
	rootPassword = "minioadmin123"
	apiPort      = 9000

	bucket     = "gvtest"
	objContent = "gvisor-object-content"

	readyTimeout = 2 * time.Minute
	pollInterval = 2 * time.Second
)

func TestMinIO(t *testing.T) {
	ctx := context.Background()

	// MinIO server.
	s := dockerutil.MakeContainer(ctx, t)
	defer s.CleanUp(ctx)
	if err := s.Spawn(ctx, dockerutil.RunOpts{
		Image: minioImage,
		Env: []string{
			"MINIO_ROOT_USER=" + rootUser,
			"MINIO_ROOT_PASSWORD=" + rootPassword,
		},
	}, "server", "/data", "--console-address", ":9001"); err != nil {
		t.Fatalf("failed to start minio: %v", err)
	}

	ip, err := s.FindIP(ctx, false)
	if err != nil {
		t.Fatalf("failed to find minio IP: %v", err)
	}

	// Wait for MinIO to report ready.
	compatibility.Poll(ctx, t, "minio to be ready", readyTimeout, pollInterval, func() error {
		status, _, err := compatibility.Get(fmt.Sprintf("http://%s:%d/minio/health/ready", ip.String(), apiPort))
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("GET /minio/health/ready: status %d", status)
		}
		return nil
	})

	// Use the mc client to create a bucket, write an object, and read it back.
	mc := dockerutil.MakeContainer(ctx, t)
	defer mc.CleanUp(ctx)
	script := fmt.Sprintf("mc mb local/%s && "+
		"echo -n '%s' | mc pipe local/%s/obj.txt && "+
		"mc cat local/%s/obj.txt", bucket, objContent, bucket, bucket)
	out, err := mc.Run(ctx, dockerutil.RunOpts{
		Image:      mcImage,
		Links:      []string{s.MakeLink("minio")},
		Entrypoint: []string{"sh"},
		Env:        []string{fmt.Sprintf("MC_HOST_local=http://%s:%s@minio:%d", rootUser, rootPassword, apiPort)},
	}, "-c", script)
	if err != nil {
		t.Fatalf("mc client failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, objContent) {
		t.Fatalf("object roundtrip: output missing %q; got: %s", objContent, out)
	}
	t.Logf("minio bucket create + object put/get roundtrip ok")
}

func TestMain(m *testing.M) {
	dockerutil.EnsureSupportedDockerVersion()
	flag.Parse()
	os.Exit(m.Run())
}
