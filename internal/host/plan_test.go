package host

import (
	"testing"
	"time"

	"github.com/camden-brown/garrison/internal/model"
)

func basePlan() model.Plan {
	return model.Plan{
		Image: "lloesche/valheim-server:latest",
		Env:   map[string]string{"SERVER_NAME": "huldra", "SERVER_PASS": "secret"},
		Ports: []model.PortMap{
			{Container: "2456/udp", Host: 2456},
			{Container: "2457/udp", Host: 2457},
		},
		Mounts:     []model.Mount{{Host: `D:\gameservers\valheim\data`, Container: "/config"}},
		Resources:  model.Resources{Memory: 4 << 30, CPUs: 2},
		StopSignal: "SIGINT",
		StopGrace:  120 * time.Second,
	}
}

// The property the whole thing rests on: the same plan hashes the same, every
// time, in the same process and the next one. Map iteration order is random,
// so a hash that walked a map directly would report the entire fleet as
// drifted on roughly every other startup.
func TestPlanHashIsStableAcrossMapOrder(t *testing.T) {
	want := PlanHash(basePlan())
	for i := 0; i < 100; i++ {
		if got := PlanHash(basePlan()); got != want {
			t.Fatalf("iteration %d: hash = %q, want %q", i, got, want)
		}
	}
}

func TestPlanHashIgnoresSliceOrderThatDoesNotMatter(t *testing.T) {
	p := basePlan()
	p.Ports = []model.PortMap{p.Ports[1], p.Ports[0]}
	if got, want := PlanHash(p), PlanHash(basePlan()); got != want {
		t.Errorf("reordering ports changed the hash: %q vs %q", got, want)
	}
}

func TestPlanHashChangesWithEveryFieldThatNeedsARecreate(t *testing.T) {
	tests := []struct {
		name  string
		mutfn func(*model.Plan)
	}{
		{"image", func(p *model.Plan) { p.Image = "other:1" }},
		{"cmd", func(p *model.Plan) { p.Cmd = []string{"-x"} }},
		{"env value", func(p *model.Plan) { p.Env["SERVER_NAME"] = "other" }},
		{"env key", func(p *model.Plan) { p.Env["NEW"] = "1" }},
		{"host port", func(p *model.Plan) { p.Ports[0].Host = 3456 }},
		{"protocol", func(p *model.Plan) { p.Ports[0].Container = "2456/tcp" }},
		{"mount source", func(p *model.Plan) { p.Mounts[0].Host = `E:\elsewhere` }},
		{"mount readonly", func(p *model.Plan) { p.Mounts[0].ReadOnly = true }},
		{"memory", func(p *model.Plan) { p.Resources.Memory = 8 << 30 }},
		{"cpus", func(p *model.Plan) { p.Resources.CPUs = 4 }},
		{"stop signal", func(p *model.Plan) { p.StopSignal = "SIGTERM" }},
		{"stop grace", func(p *model.Plan) { p.StopGrace = 60 * time.Second }},
		{"healthcheck added", func(p *model.Plan) {
			p.Health = &model.HealthCheck{Test: []string{"CMD", "true"}, Retries: 3}
		}},
	}

	base := PlanHash(basePlan())
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := basePlan()
			tt.mutfn(&p)
			if got := PlanHash(p); got == base {
				t.Errorf("changing %s did not change the hash", tt.name)
			}
		})
	}
}

// Length prefixing is what stops these two colliding. Without it both encode
// to the same byte stream and a config change goes unnoticed.
func TestPlanHashSeparatesAmbiguousEnvEncodings(t *testing.T) {
	a, b := basePlan(), basePlan()
	a.Env = map[string]string{"A": "B=C"}
	b.Env = map[string]string{"A=B": "C"}

	if PlanHash(a) == PlanHash(b) {
		t.Error(`Env{"A": "B=C"} and Env{"A=B": "C"} hash the same`)
	}
}

// The hash is stamped on the container as a label, so it cannot be an input to
// itself, and identity labels are not shape.
func TestPlanHashIgnoresGarrisonsOwnLabels(t *testing.T) {
	base := PlanHash(basePlan())

	p := basePlan()
	p.Labels = map[string]string{
		LabelManaged:  "1",
		LabelInstance: "valheim-huldra",
		LabelGame:     "valheim",
		LabelPlanHash: "whatever",
	}
	if got := PlanHash(p); got != base {
		t.Errorf("garrison labels changed the hash: %q vs %q", got, base)
	}

	p.Labels["com.example.team"] = "ops"
	if got := PlanHash(p); got == base {
		t.Error("a non-garrison label was ignored, but it is part of the container shape")
	}
}

// Windows paths contain the colon that an obvious encoding would use as a
// delimiter, so these two distinct mounts are exactly the collision that
// joining components would produce.
func TestPlanHashSeparatesAmbiguousMountEncodings(t *testing.T) {
	a, b := basePlan(), basePlan()
	a.Mounts = []model.Mount{{Host: `D:\data`, Container: "/config"}}
	b.Mounts = []model.Mount{{Host: `D`, Container: `\data:/config`}}

	if PlanHash(a) == PlanHash(b) {
		t.Error(`Mount{D:\data -> /config} and Mount{D -> \data:/config} hash the same`)
	}
}
