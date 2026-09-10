package zomboid

import (
	"fmt"
	"sort"
	"strings"

	"github.com/camden-brown/garrison/internal/model"
)

// The two syntaxes, which is the thing M3 was for.
//
// Valheim compiles to nothing, so Compile's signature was satisfied by
// returning an empty slice and the interface never had to be right about
// anything. Project Zomboid writes an INI file of 144 keys and a Lua table of
// 269, nested, and both have to come out byte-for-byte parseable by a game
// that will not tell you which line it disliked.
//
// Both files are written **whole**, from the game's own defaults overlaid with
// the instance's settings. That is the important decision and ADR 0011 records
// why: a partial file is not valid to this server, and a file assembled from
// only the keys Garrison models would delete the other four hundred.

// serverConfigName is the base name the server uses for its config, and it is
// not the instance name.
//
// The image starts the server with -servername "$SERVER_NAME", and everything
// it writes is named after that: servertest.ini, servertest_SandboxVars.lua.
// Garrison pins it to "servertest" rather than the instance name so that the
// paths Compile returns are the paths the image reads — the alternative is a
// plugin that has to know what the image did with a variable it did not set.
const serverConfigName = "servertest"

// Compile writes both of Project Zomboid's config files.
func (Game) Compile(inst model.Instance) ([]model.File, error) {
	ini, err := compileINI(inst)
	if err != nil {
		return nil, err
	}
	lua, err := compileSandbox(inst)
	if err != nil {
		return nil, err
	}

	return []model.File{
		{Path: "Server/" + serverConfigName + ".ini", Mode: 0o644, Data: ini},
		{Path: "Server/" + serverConfigName + "_SandboxVars.lua", Mode: 0o644, Data: lua},
	}, nil
}

// compileINI writes servertest.ini: one key=value per line, in the order the
// server itself writes them.
//
// Order is preserved from the extracted fixture rather than sorted, so a diff
// against a file the server wrote is a diff about values and not about
// arrangement. That matters because the settings screen shows this diff.
func compileINI(inst model.Instance) ([]byte, error) {
	var b strings.Builder
	b.WriteString("# Written by Garrison. Hand edits are replaced on the next apply.\n")

	for _, s := range iniSettings {
		v, err := iniValue(inst, s)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, "%s=%s\n", s.Key, v)
	}
	return []byte(b.String()), nil
}

// iniValue renders one key the way the server's own parser expects it: bare
// true/false, bare numbers, and unquoted strings.
func iniValue(inst model.Instance, s setting) (string, error) {
	raw, ok := lookup(inst, s.Key)
	if !ok {
		raw = s.Default
	}

	switch s.Type {
	case "bool":
		switch v := raw.(type) {
		case bool:
			if v {
				return "true", nil
			}
			return "false", nil
		case string:
			if v == "true" || v == "false" {
				return v, nil
			}
		}
		return "", fmt.Errorf("%s: %v is not true or false", s.Key, raw)

	case "int", "float":
		switch raw.(type) {
		case int, int32, int64, float32, float64:
			return trimFloat(raw), nil
		case string:
			// TOML can only carry what somebody typed, and a number typed
			// as a string is still a number to this file.
			if v := raw.(string); v != "" {
				return v, nil
			}
			return trimFloat(s.Default), nil
		}
		return "", fmt.Errorf("%s: %v is not a number", s.Key, raw)
	}

	// Strings go through as written. A newline would end the line early and
	// silently move the rest into a key the server does not know, so it is
	// refused rather than escaped — this file has no escape for it.
	out := fmt.Sprint(raw)
	if strings.ContainsAny(out, "\r\n") {
		return "", fmt.Errorf("%s: a line break cannot go in servertest.ini", s.Key)
	}
	return out, nil
}

// compileSandbox writes servertest_SandboxVars.lua.
//
// Lua, not INI, and the nesting is real: the server reads this file with a Lua
// interpreter, so it has to be a valid table literal. The keys come out sorted
// within each table for a stable diff, since unlike the ini there is no
// authored order to preserve — the game's preset order is alphabetical by
// accident rather than by intent.
func compileSandbox(inst model.Instance) ([]byte, error) {
	// Group the dotted keys back into the tables they came from.
	type group struct {
		name string
		keys []setting
	}
	flat := []setting{}
	nested := map[string][]setting{}
	var order []string

	for _, s := range sandboxSettings {
		table, key, isNested := strings.Cut(s.Key, ".")
		if !isNested {
			flat = append(flat, s)
			continue
		}
		if _, seen := nested[table]; !seen {
			order = append(order, table)
		}
		inner := s
		inner.Key = key
		nested[table] = append(nested[table], inner)
	}
	sort.Strings(order)

	var b strings.Builder
	b.WriteString("-- Written by Garrison. Hand edits are replaced on the next apply.\n")
	b.WriteString("SandboxVars = {\n")

	for _, s := range flat {
		v, err := luaValue(inst, sandboxPrefix+s.Key, s)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, "    %s = %s,\n", s.Key, v)
	}

	for _, table := range order {
		fmt.Fprintf(&b, "    %s = {\n", table)
		for _, s := range nested[table] {
			v, err := luaValue(inst, sandboxPrefix+table+"."+s.Key, s)
			if err != nil {
				return nil, err
			}
			fmt.Fprintf(&b, "        %s = %s,\n", s.Key, v)
		}
		b.WriteString("    },\n")
	}

	b.WriteString("}\n")
	return []byte(b.String()), nil
}

// luaValue renders one sandbox value as a Lua literal.
func luaValue(inst model.Instance, settingsKey string, s setting) (string, error) {
	raw, ok := lookup(inst, settingsKey)
	if !ok {
		raw = s.Default
	}

	switch s.Type {
	case "bool":
		switch v := raw.(type) {
		case bool:
			if v {
				return "true", nil
			}
			return "false", nil
		case string:
			if v == "true" || v == "false" {
				return v, nil
			}
		}
		return "", fmt.Errorf("%s: %v is not true or false", settingsKey, raw)

	case "int", "float":
		switch raw.(type) {
		case int, int32, int64, float32, float64:
			return trimFloat(raw), nil
		case string:
			if v := raw.(string); v != "" {
				return v, nil
			}
			return trimFloat(s.Default), nil
		}
		return "", fmt.Errorf("%s: %v is not a number", settingsKey, raw)
	}

	// A Lua string, quoted and escaped. Unlike the ini this file has an
	// escape, so a quote in a welcome message is representable rather than
	// a syntax error the server reports as a missing sandbox file.
	return luaQuote(fmt.Sprint(raw)), nil
}

func luaQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// trimFloat renders a number without the trailing ".0" that TOML's float
// decoding adds to whole numbers.
//
// It matters more than it looks: the sandbox file is compared against one the
// server wrote, and "Zombies = 4.0" against "Zombies = 4" is a diff on every
// integer setting in the file. A diff that is noise is a diff nobody reads.
func trimFloat(v any) string {
	switch n := v.(type) {
	case float32:
		return trimFloat(float64(n))
	case float64:
		if n == float64(int64(n)) {
			return fmt.Sprintf("%d", int64(n))
		}
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", n), "0"), ".")
	default:
		return fmt.Sprint(v)
	}
}
