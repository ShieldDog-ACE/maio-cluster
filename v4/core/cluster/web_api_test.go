package cluster

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"

	"github.com/ShieldDog-ACE/maio-cluster/v4/web"
)

func TestWebLoginAndAssets(t *testing.T) {
	c, cancel := testCoordinator(t)
	defer cancel()
	defer c.Close()
	base := "http://" + c.Addr()

	// Without the console installed, "/" is a helpful 503.
	resp, err := http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("index before install = %d", resp.StatusCode)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if _, err := webui.Install(c.cfg.Agent.DataDir, false); err != nil {
		t.Fatal(err)
	}
	resp, err = http.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "maio") {
		t.Fatalf("index after install = %d", resp.StatusCode)
	}
	resp, err = http.Get(base + "/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("app.js = %d", resp.StatusCode)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar}

	// Wrong token.
	payload, _ := json.Marshal(map[string]string{"token": "wrong"})
	resp, err = hc.Post(base+"/v1/web/login", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token = %d", resp.StatusCode)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	// Correct token sets the session cookie.
	payload, _ = json.Marshal(map[string]string{"token": c.cfg.Cluster.Token})
	resp, err = hc.Post(base+"/v1/web/login", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login = %d", resp.StatusCode)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	resp, err = hc.Get(base + "/v1/web/me")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("me = %d", resp.StatusCode)
	}
	var me struct {
		OK      bool        `json:"ok"`
		Cluster ClusterInfo `json:"cluster"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !me.OK || me.Cluster.ID != c.cfg.Cluster.ID {
		t.Fatalf("me = %+v", me)
	}

	// Cookie auth also unlocks the regular cluster API.
	resp, err = hc.Get(base + "/v1/nodes")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("nodes with cookie = %d", resp.StatusCode)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}
