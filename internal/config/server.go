// Package config reads and writes what Garrison knows that Docker does not.
//
// One file per server, human-readable TOML, editable with the tool closed —
// which is the point. A dashboard that owns its configuration in a format only
// it can read is a dashboard you cannot fix when it will not start.
//
// This package sits beside services: it does I/O, and nothing above it reaches
// for a file. It imports model and nothing else of ours.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/camden-brown/garrison/internal/model"
)

// Server is the on-disk form of one instance.
//
// It is a separate type from model.Instance rather than TOML tags on the model
// because the file is a contract with a person: it outlives any particular
// version of the struct, and a field renamed in code should not silently stop
// reading a file somebody hand-edited.
type Server struct {
	Game    string `toml:"game"`
	Image   string `toml:"image,omitempty"`
	Data    string `toml:"data,omitempty"`
	Volume  string `toml:"volume,omitempty"`
	Address string `toml:"address,omitempty"`

	Resources Resources      `toml:"resources,omitempty"`
	Ports     []Port         `toml:"ports,omitempty"`
	Settings  map[string]any `toml:"settings,omitempty"`
	Mods      []Mod          `toml:"mods,omitempty"`
	Schedules []Schedule     `toml:"schedule,omitempty"`
}

type Resources struct {
	Memory string  `toml:"memory,omitempty"` // "4GiB", human-written
	CPUs   float64 `toml:"cpus,omitempty"`
}

type Port struct {
	Container string `toml:"container"` // "16261/udp" — protocol included
	Host      int    `toml:"host"`
}

type Mod struct {
	ID  string `toml:"id"`
	Pin string `toml:"pin,omitempty"`
}

type Schedule struct {
	Kind   string `toml:"kind"`
	Cron   string `toml:"cron"`
	Drain  string `toml:"drain,omitempty"`
	Policy string `toml:"policy,omitempty"`
}

// Store is a directory of server files.
type Store struct {
	Dir string
}

// ErrNotFound is returned for a server with no file.
var ErrNotFound = errors.New("no configuration for that server")

// Path is where a server's file lives.
func (s Store) Path(name string) string {
	return filepath.Join(s.Dir, name+".toml")
}

// Load reads one server.
func (s Store) Load(name string) (model.Instance, error) {
	var raw Server
	if _, err := toml.DecodeFile(s.Path(name), &raw); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return model.Instance{}, fmt.Errorf("%s: %w", name, ErrNotFound)
		}
		return model.Instance{}, fmt.Errorf("%s: reading %s: %w", name, s.Path(name), err)
	}

	inst, err := raw.instance(name)
	if err != nil {
		return model.Instance{}, fmt.Errorf("%s: %w", name, err)
	}
	return inst, nil
}

// LoadAll reads every server file in the directory, newest names first being
// irrelevant — they come back sorted by name so the fleet order is stable.
//
// A file that will not parse does not stop the others. One malformed server
// should not take the whole dashboard down, so it comes back as an error
// alongside the instances that did load, and the caller reports it.
func (s Store) LoadAll() ([]model.Instance, []error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// No directory yet is not a problem: it is a first run.
			return nil, nil
		}
		return nil, []error{fmt.Errorf("reading %s: %w", s.Dir, err)}
	}

	var out []model.Instance
	var problems []error
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".toml")
		inst, err := s.Load(name)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		out = append(out, inst)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, problems
}

// Save writes one server.
//
// It writes to a temporary file and renames, so an interrupted write leaves
// the previous configuration intact rather than a half-written file that will
// not parse. Losing power during a settings apply should cost the change, not
// the server.
func (s Store) Save(inst model.Instance) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return fmt.Errorf("%s: creating %s: %w", inst.Name, s.Dir, err)
	}

	tmp, err := os.CreateTemp(s.Dir, "."+inst.Name+".*.toml")
	if err != nil {
		return fmt.Errorf("%s: creating a temporary file: %w", inst.Name, err)
	}
	defer os.Remove(tmp.Name())

	if err := toml.NewEncoder(tmp).Encode(fromInstance(inst)); err != nil {
		tmp.Close()
		return fmt.Errorf("%s: encoding: %w", inst.Name, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("%s: flushing: %w", inst.Name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("%s: closing: %w", inst.Name, err)
	}

	if err := os.Rename(tmp.Name(), s.Path(inst.Name)); err != nil {
		return fmt.Errorf("%s: replacing %s: %w", inst.Name, s.Path(inst.Name), err)
	}
	return nil
}

// Delete removes a server's file. A missing file is not an error: the caller
// wanted it gone and it is.
func (s Store) Delete(name string) error {
	if err := os.Remove(s.Path(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s: removing %s: %w", name, s.Path(name), err)
	}
	return nil
}

func (raw Server) instance(name string) (model.Instance, error) {
	if raw.Game == "" {
		return model.Instance{}, errors.New(`no "game" set, so Garrison cannot tell which plugin owns it`)
	}

	memory, err := ParseBytes(raw.Resources.Memory)
	if err != nil {
		return model.Instance{}, fmt.Errorf("resources.memory: %w", err)
	}

	inst := model.Instance{
		Name:      name,
		Game:      raw.Game,
		Image:     raw.Image,
		Data:      raw.Data,
		Volume:    raw.Volume,
		Address:   raw.Address,
		Resources: model.Resources{Memory: memory, CPUs: raw.Resources.CPUs},
		Settings:  raw.Settings,
	}
	if inst.Settings == nil {
		inst.Settings = map[string]any{}
	}

	for _, p := range raw.Ports {
		inst.Ports = append(inst.Ports, model.PortMap{Container: p.Container, Host: p.Host})
	}
	for _, m := range raw.Mods {
		inst.Mods = append(inst.Mods, model.ModRef{ID: m.ID, Pin: m.Pin})
	}
	for _, sch := range raw.Schedules {
		drain, err := time.ParseDuration(orZero(sch.Drain))
		if sch.Drain != "" && err != nil {
			return model.Instance{}, fmt.Errorf("schedule %q: drain: %w", sch.Kind, err)
		}
		inst.Schedules = append(inst.Schedules, model.Schedule{
			Kind: sch.Kind, Cron: sch.Cron, Drain: drain, Policy: sch.Policy,
		})
	}
	return inst, nil
}

func fromInstance(inst model.Instance) Server {
	raw := Server{
		Game:      inst.Game,
		Image:     inst.Image,
		Data:      inst.Data,
		Volume:    inst.Volume,
		Address:   inst.Address,
		Resources: Resources{Memory: FormatBytes(inst.Resources.Memory), CPUs: inst.Resources.CPUs},
		Settings:  inst.Settings,
	}
	for _, p := range inst.Ports {
		raw.Ports = append(raw.Ports, Port{Container: p.Container, Host: p.Host})
	}
	for _, m := range inst.Mods {
		raw.Mods = append(raw.Mods, Mod{ID: m.ID, Pin: m.Pin})
	}
	for _, s := range inst.Schedules {
		drain := ""
		if s.Drain > 0 {
			drain = s.Drain.String()
		}
		raw.Schedules = append(raw.Schedules, Schedule{
			Kind: s.Kind, Cron: s.Cron, Drain: drain, Policy: s.Policy,
		})
	}
	return raw
}

func orZero(s string) string {
	if s == "" {
		return "0s"
	}
	return s
}
