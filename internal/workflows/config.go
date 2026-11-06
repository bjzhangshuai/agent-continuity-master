package workflows

import (
	"encoding/json"
	"errors"
	"github.com/agent-continuity/agent-continuity-master/internal/commerce"
	"github.com/agent-continuity/agent-continuity-master/internal/orchestration"
	"net/url"
	"os"
	"strings"
)

type Config struct {
	Demo      bool `json:"demo_enabled"`
	AllowLive bool `json:"allow_live"`
	Agents    map[string]struct {
		URL       string `json:"url"`
		TokenFile string `json:"token_file"`
	} `json:"agents"`
	CommerceURL       string `json:"commerce_url"`
	CommerceTokenFile string `json:"commerce_token_file"`
}

func Build(raw string) (map[string]orchestration.Executor, commerce.Gateway, bool, error) {
	agents := map[string]orchestration.Executor{}
	if raw == "" {
		return agents, nil, false, nil
	}
	var c Config
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(&c); e != nil {
		return nil, nil, false, e
	}
	if c.Demo && c.AllowLive {
		return nil, nil, false, errors.New("demo and live workflows cannot be mixed")
	}
	if c.Demo {
		for _, name := range []string{"default", "researcher", "reviewer", "writer"} {
			agents[name] = orchestration.DemoAgent{Name: name}
		}
	}
	validURL := func(raw string) bool {
		u, e := url.Parse(raw)
		return e == nil && u.User == nil && u.Fragment == "" && u.RawQuery == "" && u.Host != "" && (u.Scheme == "https" || (c.Demo && u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost")))
	}
	for name, a := range c.Agents {
		if name == "" || !validURL(a.URL) || a.TokenFile == "" || !c.AllowLive && !c.Demo {
			return nil, nil, false, errors.New("remote agents need configured HTTPS URL, credentials and live opt-in")
		}
		if _, e := os.Stat(a.TokenFile); e != nil {
			return nil, nil, false, errors.New("agent credential file unavailable")
		}
		agents[name] = orchestration.RemoteA2A{URL: a.URL, TokenFile: a.TokenFile}
	}
	var g commerce.Gateway
	if c.Demo {
		g = commerce.DemoGateway{}
	}
	if c.CommerceURL != "" {
		if !validURL(c.CommerceURL) || c.CommerceTokenFile == "" || !c.AllowLive && !c.Demo {
			return nil, nil, false, errors.New("commerce gateway requires URL, credential file and live opt-in")
		}
		b, e := os.ReadFile(c.CommerceTokenFile)
		if e != nil || len(strings.TrimSpace(string(b))) < 16 {
			return nil, nil, false, errors.New("invalid commerce gateway credential")
		}
		g = commerce.HTTPGateway{URL: c.CommerceURL, Token: strings.TrimSpace(string(b)), Simulated: c.Demo}
	}
	return agents, g, c.Demo, nil
}
