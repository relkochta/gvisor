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

// Package gitlab is a gVisor compatibility test for the GitLab CE image.
//
// The GitLab version under test is pinned in
// images/compatibility/gitlab/gitlab/Dockerfile.
package gitlab

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
	gitlabImage = "compatibility/gitlab/gitlab"

	gitlabPort = 80

	rootPassword = "xQ7kZ2mvP9wL4n"
	rootToken    = "gvisor-compat-token-1234" // minted via the Rails console below.
	projectKey   = "gvisor-test"

	// GitLab's first boot is pretty slow.
	bootTimeout  = 16 * time.Minute
	apiTimeout   = 3 * time.Minute
	pollInterval = 5 * time.Second
)

func TestGitLab(t *testing.T) {
	ctx := context.Background()

	c := dockerutil.MakeContainer(ctx, t)
	defer c.CleanUp(ctx)
	if err := c.Spawn(ctx, dockerutil.RunOpts{
		Image: gitlabImage,
		Env: []string{
			"GITLAB_OMNIBUS_CONFIG=" + strings.Join([]string{
				"external_url 'http://localhost'",
				"gitlab_rails['initial_root_password'] = '" + rootPassword + "'",
				// Help reduce resource usage for the test.
				"prometheus_monitoring['enable'] = false",
				"puma['worker_processes'] = 2",
				"sidekiq['max_concurrency'] = 5",
				"nginx['worker_processes'] = 2",
			}, "\n"),
		},
	}); err != nil {
		t.Fatalf("failed to start gitlab: %v", err)
	}

	ip, err := c.FindIP(ctx, false)
	if err != nil {
		t.Fatalf("failed to find gitlab IP: %v", err)
	}
	base := fmt.Sprintf("http://%s:%d", ip.String(), gitlabPort)
	const host = "localhost"

	// Wait for the web app to come up.
	compatibility.Poll(ctx, t, "gitlab web to be ready", bootTimeout, pollInterval, func() error {
		status, _, err := compatibility.Request{URL: base + "/users/sign_in", Host: host}.Do()
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("GET /users/sign_in: status %d", status)
		}
		return nil
	})

	// Mint a personal access token for root via the Rails console.
	mintToken(ctx, t, c)

	api := func(method, path, body string, want int) string {
		return compatibility.Request{
			Method:      method,
			URL:         base + path,
			Host:        host,
			ContentType: "application/json",
			Headers:     map[string]string{"PRIVATE-TOKEN": rootToken},
			Body:        body,
			Timeout:     apiTimeout,
		}.DoOrFatal(t, want)
	}

	// Create a project.
	api(http.MethodPost, "/api/v4/projects",
		fmt.Sprintf(`{"name":%q,"initialize_with_readme":true,"visibility":"private"}`, projectKey),
		http.StatusCreated)

	// The project is addressable as root/<projectKey>; URL-encode the slash.
	projPath := "root%2F" + projectKey

	// Commit a file via the API.
	commitBody := `{"branch":"main","content":"gvisor compatibility test","commit_message":"add gvisor.txt"}`
	api(http.MethodPost, "/api/v4/projects/"+projPath+"/repository/files/gvisor.txt", commitBody, http.StatusCreated)

	// Read the file back via the API.
	got := api(http.MethodGet, "/api/v4/projects/"+projPath+"/repository/files/gvisor.txt?ref=main", "", http.StatusOK)
	if !strings.Contains(got, `"file_name":"gvisor.txt"`) {
		t.Fatalf("read file back: unexpected body %s", got)
	}
	t.Logf("gitlab created project %q and committed/read a file", projectKey)
}

// mintToken creates a known personal access token for the root user via the
// GitLab Rails console.
func mintToken(ctx context.Context, t *testing.T, c *dockerutil.Container) {
	t.Helper()
	script := fmt.Sprintf(`u = User.find_by_username('root'); `+
		`t = u.personal_access_tokens.create!(scopes: ['api'], name: 'gvisor', expires_at: 365.days.from_now); `+
		`t.set_token(%q); t.save!`, rootToken)
	// May need to wait for the Rails console to be ready.
	compatibility.Poll(ctx, t, "gitlab rails token creation", apiTimeout, pollInterval, func() error {
		out, err := c.Exec(ctx, dockerutil.ExecOpts{}, "gitlab-rails", "runner", script)
		if err != nil {
			return fmt.Errorf("gitlab-rails runner: %v (%s)", err, out)
		}
		return nil
	})
}

func TestMain(m *testing.M) {
	dockerutil.EnsureSupportedDockerVersion()
	flag.Parse()
	os.Exit(m.Run())
}
