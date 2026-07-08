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

// Package elasticsearch is a gVisor compatibility test for Elasticsearch.
//
// The Elasticsearch version under test is pinned in
// images/compatibility/elasticsearch/elasticsearch/Dockerfile.
package elasticsearch

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
	elasticsearchImage = "compatibility/elasticsearch/elasticsearch"
	esPort             = 9200

	docTitle = "gVisor"

	readyTimeout = 3 * time.Minute
	pollInterval = 3 * time.Second
)

func TestElasticsearch(t *testing.T) {
	ctx := context.Background()

	c := dockerutil.MakeContainer(ctx, t)
	defer c.CleanUp(ctx)
	if err := c.Spawn(ctx, dockerutil.RunOpts{
		Image: elasticsearchImage,
		Env: []string{
			"discovery.type=single-node",
			"xpack.security.enabled=false",
			"ES_JAVA_OPTS=-Xms512m -Xmx512m",
			"bootstrap.memory_lock=false",
		},
	}); err != nil {
		t.Fatalf("failed to start elasticsearch: %v", err)
	}

	ip, err := c.FindIP(ctx, false)
	if err != nil {
		t.Fatalf("failed to find elasticsearch IP: %v", err)
	}
	base := fmt.Sprintf("http://%s:%d", ip.String(), esPort)

	// Wait for the REST API to come up.
	compatibility.Poll(ctx, t, "elasticsearch HTTP to be ready", readyTimeout, pollInterval, func() error {
		status, _, err := compatibility.Get(base + "/_cluster/health")
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("GET /_cluster/health: status %d", status)
		}
		return nil
	})

	// Create an index.
	compatibility.Request{
		Method:      http.MethodPut,
		URL:         base + "/books",
		ContentType: "application/json",
		Body:        `{"settings":{"number_of_shards":1,"number_of_replicas":0}}`,
	}.DoOrFatal(t, http.StatusOK)

	// Index a document (refresh so it is immediately searchable).
	compatibility.Request{
		Method:      http.MethodPost,
		URL:         base + "/books/_doc/1?refresh=true",
		ContentType: "application/json",
		Body:        fmt.Sprintf(`{"title":%q,"author":"sandbox","year":2026}`, docTitle),
	}.DoOrFatal(t, http.StatusCreated)

	// Search for it back.
	got := compatibility.Request{URL: base + "/books/_search?q=title:gVisor"}.DoOrFatal(t, http.StatusOK)
	if !strings.Contains(got, docTitle) {
		t.Fatalf("search: expected to find %q; got: %s", docTitle, got)
	}
	t.Logf("elasticsearch roundtrip ok")
}

func TestMain(m *testing.M) {
	dockerutil.EnsureSupportedDockerVersion()
	flag.Parse()
	os.Exit(m.Run())
}
