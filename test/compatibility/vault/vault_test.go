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

// Package vault is a gVisor compatibility test for HashiCorp Vault.
//
// The Vault version under test is pinned in
// images/compatibility/vault/vault/Dockerfile.
package vault

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	units "github.com/docker/go-units"

	"gvisor.dev/gvisor/pkg/test/dockerutil"
	"gvisor.dev/gvisor/test/compatibility"
)

const (
	vaultImage = "compatibility/vault/vault"
	vaultPort  = 8200

	localConfig = `{"storage":{"file":{"path":"/vault/file"}},` +
		`"listener":[{"tcp":{"address":"0.0.0.0:8200","tls_disable":true}}],` +
		`"disable_mlock":false,"ui":true}`

	secret = "gvisor-secret"

	readyTimeout = 2 * time.Minute
	pollInterval = 2 * time.Second
)

func TestVault(t *testing.T) {
	ctx := context.Background()

	c := dockerutil.MakeContainer(ctx, t)
	defer c.CleanUp(ctx)
	if err := c.Spawn(ctx, dockerutil.RunOpts{
		Image:   vaultImage,
		Env:     []string{"VAULT_LOCAL_CONFIG=" + localConfig},
		CapAdd:  []string{"IPC_LOCK"},
		Ulimits: []*units.Ulimit{{Name: "memlock", Soft: -1, Hard: -1}},
	}, "server"); err != nil {
		t.Fatalf("failed to start vault: %v", err)
	}

	ip, err := c.FindIP(ctx, false)
	if err != nil {
		t.Fatalf("failed to find vault IP: %v", err)
	}
	base := fmt.Sprintf("http://%s:%d", ip.String(), vaultPort)

	// hdr carries the Vault token once we have it.
	hdr := map[string]string{}
	api := func(method, path, body string, want int) string {
		return compatibility.Request{
			Method: method, URL: base + path, Body: body, Headers: hdr,
		}.DoOrFatal(t, want)
	}

	// Wait for the server to answer (it comes up sealed/uninitialized).
	compatibility.Poll(ctx, t, "vault to respond", readyTimeout, pollInterval, func() error {
		status, _, err := compatibility.Get(base + "/v1/sys/seal-status")
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("GET /v1/sys/seal-status: status %d", status)
		}
		return nil
	})

	// Initialize with a single unseal key.
	var init struct {
		Keys      []string `json:"keys_base64"`
		RootToken string   `json:"root_token"`
	}
	if err := json.Unmarshal([]byte(api(http.MethodPost, "/v1/sys/init",
		`{"secret_shares":1,"secret_threshold":1}`, http.StatusOK)), &init); err != nil {
		t.Fatalf("init: bad JSON: %v", err)
	}
	if len(init.Keys) == 0 || init.RootToken == "" {
		t.Fatalf("init: missing keys or root token")
	}
	hdr["X-Vault-Token"] = init.RootToken

	// Unseal.
	api(http.MethodPost, "/v1/sys/unseal", fmt.Sprintf(`{"key":%q}`, init.Keys[0]), http.StatusOK)

	// Enable and exercise the KV v2 engine.
	api(http.MethodPost, "/v1/sys/mounts/secret", `{"type":"kv","options":{"version":"2"}}`, http.StatusNoContent)
	api(http.MethodPost, "/v1/secret/data/gv", fmt.Sprintf(`{"data":{"k":%q}}`, secret), http.StatusOK)
	if got := api(http.MethodGet, "/v1/secret/data/gv", "", http.StatusOK); !strings.Contains(got, secret) {
		t.Fatalf("KV read back: missing %q; body: %s", secret, got)
	}

	// Enable and exercise the transit engine.
	api(http.MethodPost, "/v1/sys/mounts/transit", `{"type":"transit"}`, http.StatusNoContent)
	api(http.MethodPost, "/v1/transit/keys/k1", `{}`, http.StatusOK)
	pt := base64.StdEncoding.EncodeToString([]byte(secret))
	var enc struct {
		Data struct {
			Ciphertext string `json:"ciphertext"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(api(http.MethodPost, "/v1/transit/encrypt/k1",
		fmt.Sprintf(`{"plaintext":%q}`, pt), http.StatusOK)), &enc); err != nil {
		t.Fatalf("encrypt: bad JSON: %v", err)
	}
	var dec struct {
		Data struct {
			Plaintext string `json:"plaintext"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(api(http.MethodPost, "/v1/transit/decrypt/k1",
		fmt.Sprintf(`{"ciphertext":%q}`, enc.Data.Ciphertext), http.StatusOK)), &dec); err != nil {
		t.Fatalf("decrypt: bad JSON: %v", err)
	}
	roundtrip, err := base64.StdEncoding.DecodeString(dec.Data.Plaintext)
	if err != nil {
		t.Fatalf("decrypt: bad base64: %v", err)
	}
	if string(roundtrip) != secret {
		t.Fatalf("transit roundtrip: got %q, want %q", roundtrip, secret)
	}
	t.Logf("vault KV and transit roundtrips ok")
}

func TestMain(m *testing.M) {
	dockerutil.EnsureSupportedDockerVersion()
	flag.Parse()
	os.Exit(m.Run())
}
