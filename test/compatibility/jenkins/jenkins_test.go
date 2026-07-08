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

// Package jenkins is a gVisor compatibility test for the Jenkins automation server.
//
// The Jenkins version under test is pinned in
// images/compatibility/jenkins/jenkins/Dockerfile.
package jenkins

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strings"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/test/dockerutil"
	"gvisor.dev/gvisor/test/compatibility"
)

const (
	jenkinsImage = "compatibility/jenkins/jenkins"
	jenkinsPort  = 8080
	jobName      = "gvisor-test"
	buildMarker  = "gvisor-build-ok"

	readyTimeout = 3 * time.Minute
	buildTimeout = 2 * time.Minute
	pollInterval = 2 * time.Second
)

// A minimal freestyle job with a single shell step.
const jobConfigXML = `<?xml version='1.1' encoding='UTF-8'?>
<project>
  <builders>
    <hudson.tasks.Shell>
      <command>echo ` + buildMarker + `</command>
    </hudson.tasks.Shell>
  </builders>
</project>`

func TestJenkins(t *testing.T) {
	ctx := context.Background()

	c := dockerutil.MakeContainer(ctx, t)
	defer c.CleanUp(ctx)
	if err := c.Spawn(ctx, dockerutil.RunOpts{
		Image: jenkinsImage,
		// Skip the setup wizard.
		Env: []string{"JAVA_OPTS=-Djenkins.install.runSetupWizard=false"},
	}); err != nil {
		t.Fatalf("failed to start jenkins: %v", err)
	}

	ip, err := c.FindIP(ctx, false)
	if err != nil {
		t.Fatalf("failed to find jenkins IP: %v", err)
	}
	base := fmt.Sprintf("http://%s:%d", ip.String(), jenkinsPort)

	// Wait for the controller to finish booting.
	compatibility.Poll(ctx, t, "jenkins API to be ready", readyTimeout, pollInterval, func() error {
		status, _, err := compatibility.Get(base + "/api/json")
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("GET /api/json: status %d", status)
		}
		return nil
	})

	// Crumb-protected POSTs must share a session, so use a cookie jar.
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	client := &http.Client{Jar: jar, Timeout: 30 * time.Second}

	crumbField, crumb := fetchCrumb(ctx, t, client, base)

	// Create the job from config.xml.
	if status, body := post(ctx, t, client, base+"/createItem?name="+jobName,
		"application/xml", jobConfigXML, crumbField, crumb); status != http.StatusOK {
		t.Fatalf("create job: status %d; body: %s", status, body)
	}

	// Trigger a build.
	if status, body := post(ctx, t, client, base+"/job/"+jobName+"/build",
		"", "", crumbField, crumb); status != http.StatusCreated {
		t.Fatalf("trigger build: status %d, want %d; body: %s", status, http.StatusCreated, body)
	}

	// Wait for the build to complete and check it succeeded.
	compatibility.Poll(ctx, t, "jenkins build to succeed", buildTimeout, pollInterval, func() error {
		status, body, err := compatibility.Get(base + "/job/" + jobName + "/lastBuild/api/json")
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("lastBuild: status %d", status)
		}
		var b struct {
			Building bool   `json:"building"`
			Result   string `json:"result"`
		}
		if err := json.Unmarshal([]byte(body), &b); err != nil {
			return fmt.Errorf("lastBuild JSON: %v", err)
		}
		if b.Building || b.Result == "" {
			return fmt.Errorf("build still running")
		}
		if b.Result != "SUCCESS" {
			t.Fatalf("build finished with result %q, want SUCCESS", b.Result)
		}
		return nil
	})

	// The console output proves the shell step actually executed.
	console := compatibility.Request{URL: base + "/job/" + jobName + "/lastBuild/consoleText"}.DoOrFatal(t, http.StatusOK)
	if !strings.Contains(console, buildMarker) {
		t.Fatalf("build console missing %q; body: %s", buildMarker, console)
	}
	t.Logf("jenkins ran job %q to SUCCESS", jobName)
}

// fetchCrumb retrieves a CSRF crumb.
func fetchCrumb(ctx context.Context, t *testing.T, client *http.Client, base string) (string, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/crumbIssuer/api/json", nil)
	if err != nil {
		t.Fatalf("crumb request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("crumb request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("crumb: status %d; body: %s", resp.StatusCode, body)
	}
	if v := resp.Header.Get("X-Jenkins"); v != "" {
		t.Logf("jenkins version: %s", v)
	}
	var c struct {
		CrumbRequestField string `json:"crumbRequestField"`
		Crumb             string `json:"crumb"`
	}
	if err := json.Unmarshal(body, &c); err != nil {
		t.Fatalf("crumb JSON: %v; body: %s", err, body)
	}
	return c.CrumbRequestField, c.Crumb
}

func post(ctx context.Context, t *testing.T, client *http.Client, url, contentType, body, crumbField, crumb string) (int, string) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, r)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if crumbField != "" {
		req.Header.Set(crumbField, crumb)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(rb)
}

func TestMain(m *testing.M) {
	dockerutil.EnsureSupportedDockerVersion()
	flag.Parse()
	os.Exit(m.Run())
}
