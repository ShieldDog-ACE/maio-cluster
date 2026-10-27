package engine

import (
	"testing"

	"github.com/ShieldDog-ACE/maio-cluster/internal/config"
)

func TestResolveOptions(t *testing.T) {
	conf := config.DefaultGlobal()
	conf.CtxSize = 8192
	conf.Parallel = 3
	conf.KVType = "q8_0"
	conf.Threads = 4
	conf.FlashAttn = "auto"
	m := New(conf)
	c := m.resolveOptions(Options{})
	if c.ctx != 8192 || c.parallel != 3 || c.kvType != "q8_0" || c.threads != 4 {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	c = m.resolveOptions(Options{Ctx: 2048, Parallel: 1, KVType: "f16", Threads: 2})
	if c.ctx != 2048 || c.parallel != 1 || c.kvType != "f16" || c.threads != 2 {
		t.Fatalf("unexpected overrides: %+v", c)
	}
}

func TestInstanceMatches(t *testing.T) {
	c := engineConfig{ctx: 4096, parallel: 2, kvType: "f16"}
	if !instanceMatches(&Instance{Context: 4096, Parallel: 2, KVType: "f16"}, c) {
		t.Fatal("matching instance rejected")
	}
	if instanceMatches(&Instance{Context: 2048, Parallel: 2, KVType: "f16"}, c) {
		t.Fatal("context mismatch accepted")
	}
	if instanceMatches(&Instance{Context: 4096, Parallel: 1, KVType: "f16"}, c) {
		t.Fatal("parallel mismatch accepted")
	}
	if instanceMatches(&Instance{Context: 4096, Parallel: 2, KVType: "q8_0"}, c) {
		t.Fatal("KV mismatch accepted")
	}
	if !instanceMatches(&Instance{Context: 4096, Parallel: 0, KVType: ""}, engineConfig{ctx: 0, parallel: 1, kvType: "f16"}) {
		t.Fatal("legacy V2 instance should match defaults")
	}
}
