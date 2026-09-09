package host

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"sort"
	"strconv"

	"github.com/camden-brown/garrison/internal/model"
)

// PlanHash is a stable digest of everything about a Plan that would require a
// new container to change.
//
// It is stamped on the container as LabelPlanHash so startup reconciliation
// can compare a running container against what its config produces today and
// report drift, rather than silently recreating something you changed by hand
// for a reason.
//
// Stability is the whole point, so the encoding is written out by hand: map
// iteration order is randomised, and struct field order is a thing a future
// edit would change without anyone noticing that every container in the fleet
// had suddenly drifted. Every component is length-prefixed and fed in
// separately — see str for why joining with a delimiter is not safe here.
func PlanHash(p model.Plan) string {
	h := sha256.New()

	str(h, "image", p.Image)
	for _, arg := range p.Cmd {
		str(h, "cmd", arg)
	}
	for _, k := range sortedKeys(p.Env) {
		str(h, "env.key", k)
		str(h, "env.value", p.Env[k])
	}

	ports := append([]model.PortMap(nil), p.Ports...)
	sort.Slice(ports, func(i, j int) bool {
		if ports[i].Container != ports[j].Container {
			return ports[i].Container < ports[j].Container
		}
		return ports[i].Host < ports[j].Host
	})
	for _, m := range ports {
		str(h, "port.container", m.Container)
		str(h, "port.host", strconv.Itoa(m.Host))
	}

	mounts := append([]model.Mount(nil), p.Mounts...)
	sort.Slice(mounts, func(i, j int) bool { return mounts[i].Container < mounts[j].Container })
	for _, m := range mounts {
		str(h, "mount.host", m.Host)
		str(h, "mount.container", m.Container)
		str(h, "mount.readonly", strconv.FormatBool(m.ReadOnly))
	}

	// Garrison's own labels are excluded deliberately: LabelPlanHash cannot
	// contain itself, and the rest are identity rather than shape.
	for _, k := range sortedKeys(p.Labels) {
		if isGarrisonLabel(k) {
			continue
		}
		str(h, "label.key", k)
		str(h, "label.value", p.Labels[k])
	}

	str(h, "memory", strconv.FormatInt(p.Resources.Memory, 10))
	str(h, "cpus", strconv.FormatFloat(p.Resources.CPUs, 'f', 3, 64))
	str(h, "stopsignal", p.StopSignal)
	str(h, "stopgrace", strconv.FormatInt(int64(p.StopGrace), 10))

	if p.Health != nil {
		for _, arg := range p.Health.Test {
			str(h, "health.test", arg)
		}
		str(h, "health.interval", strconv.FormatInt(int64(p.Health.Interval), 10))
		str(h, "health.timeout", strconv.FormatInt(int64(p.Health.Timeout), 10))
		str(h, "health.retries", strconv.Itoa(p.Health.Retries))
	}

	return hex.EncodeToString(h.Sum(nil))[:16]
}

func isGarrisonLabel(k string) bool {
	switch k {
	case LabelManaged, LabelInstance, LabelGame, LabelPlanHash:
		return true
	}
	return false
}

// str feeds one length-prefixed component to the digest.
//
// Every component goes in separately rather than joined with a separator,
// because there is no separator that cannot appear in the data. Joining a
// mount as host+":"+container looks fine until the host path is a Windows
// drive — `D:\gameserversalheim` already contains the delimiter, and two
// different mounts collapse to the same bytes. Length prefixing each part
// removes the question.
func str(h io.Writer, field, value string) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(field)+1+len(value)))
	_, _ = h.Write(n[:])
	_, _ = io.WriteString(h, field)
	_, _ = io.WriteString(h, "\x00")
	_, _ = io.WriteString(h, value)
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
