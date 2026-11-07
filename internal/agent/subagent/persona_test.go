package subagent

import (
	"slices"
	"testing"
)

// personaRegistryFixtures builds role bytes covering the full mode
// vocabulary plus a shadowing attempt on a built-in persona name — the
// same shape as delegationRegistryFixtures. The ids are all the same
// length so the map literal needs no gofmt alignment padding.
func personaRegistryFixtures() map[string][]byte {
	return map[string][]byte{
		"boss.md":  []byte("---\nname: Boss\nmode: primary\ntools: [read]\ndescription: runs the session\n---\nprimary body"),
		"work.md":  []byte("---\nname: Worker\nmode: subagent\ntools: [read]\ndescription: does work\n---\nsubagent body"),
		"flex.md":  []byte("---\nname: Flex\nmode: all\ntools: [read]\ndescription: does either\n---\nall body"),
		"base.md":  []byte("---\nname: Legacy\ntools: [read]\ndescription: old default\n---\nlegacy body"),
		"build.md": []byte("---\nname: Build\nmode: primary\ntools: [write]\ndescription: shadow build\n---\nshadow body"),
	}
}

// TestPersonas pins the yaah-21uk D4 enumeration: role files with mode
// "primary" or "all" are personas, delegate-only files (explicit
// "subagent" and the missing-key default) are not, and the synthetic
// built-ins always lead the list — a rolefile cannot displace a
// same-named built-in.
func TestPersonas(t *testing.T) {
	reg := NewRoleRegistry()
	if err := reg.LoadBytes(personaRegistryFixtures()); err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	SetDefaultRoleRegistry(reg)
	defer SetDefaultRoleRegistry(nil)

	t.Run("built-ins first, then primary and all, sorted", func(t *testing.T) {
		ps := Personas()
		names := make([]string, 0, len(ps))
		for _, p := range ps {
			names = append(names, p.Name)
		}
		want := []string{"build", "plan", "boss", "flex"}
		if !slices.Equal(names, want) {
			t.Errorf("Personas names = %v, want %v", names, want)
		}
	})

	t.Run("delegate-only files excluded", func(t *testing.T) {
		for _, p := range Personas() {
			switch p.Name {
			case "work", "base":
				t.Errorf("delegate-only role %q leaked into Personas", p.Name)
			}
		}
	})

	t.Run("registry persona carries file fields", func(t *testing.T) {
		p, ok := ResolvePersona("boss")
		if !ok {
			t.Fatal("boss did not resolve as a persona")
		}
		if p.Mode != "primary" || p.Body != "primary body" ||
			p.Description != "runs the session" || p.ReadOnly {
			t.Errorf("boss persona = %+v, want mode primary, the file body and description, not read-only", p)
		}
	})

	t.Run("built-in shadows file-defined build", func(t *testing.T) {
		count := 0
		for _, p := range Personas() {
			if p.Name != BuildPersonaName {
				continue
			}
			count++
			if p.Body != "" || p.ReadOnly || p.Description != builtinPersonas[BuildPersonaName].Description {
				t.Errorf("built-in build persona shadowed by its rolefile: %+v", p)
			}
		}
		if count != 1 {
			t.Errorf("build persona listed %d times, want exactly the built-in one", count)
		}
	})
}

// TestPersonasWithoutRegistry pins that the synthetic built-ins do not
// depend on a role registry being installed.
func TestPersonasWithoutRegistry(t *testing.T) {
	SetDefaultRoleRegistry(nil)
	ps := Personas()
	if len(ps) != 2 {
		t.Fatalf("Personas with no registry = %d entries, want the 2 built-ins", len(ps))
	}
	if ps[0].Name != BuildPersonaName || ps[1].Name != PlanPersonaName {
		t.Errorf("built-in order = [%s %s], want [build plan]", ps[0].Name, ps[1].Name)
	}
}

// TestResolvePersona pins the resolution matrix across built-ins and
// registry personas: primary and all resolve, delegate-only and
// unknown names do not, and a rolefile cannot displace a built-in.
func TestResolvePersona(t *testing.T) {
	reg := NewRoleRegistry()
	if err := reg.LoadBytes(personaRegistryFixtures()); err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	SetDefaultRoleRegistry(reg)
	defer SetDefaultRoleRegistry(nil)

	tests := []struct {
		name     string
		lookup   string
		want     bool
		wantMode string
		wantBody string
		wantDesc string
		wantRO   bool
	}{
		{"built-in build", "build", true, "primary", "", "Default coding agent. Full tool surface.", false},
		{"built-in plan", "plan", true, "primary", "", "Explores and plans without editing; read-only enforcement.", true},
		{"file role mode primary", "boss", true, "primary", "primary body", "runs the session", false},
		{"file role mode all", "flex", true, "all", "all body", "does either", false},
		{"file role mode subagent", "work", false, "", "", "", false},
		{"file role mode omitted", "base", false, "", "", "", false},
		{"unknown name", "ghost", false, "", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, ok := ResolvePersona(tt.lookup)
			if ok != tt.want {
				t.Fatalf("ResolvePersona(%q) ok = %v, want %v", tt.lookup, ok, tt.want)
			}
			if !ok {
				return
			}
			if p.Name != tt.lookup {
				t.Errorf("Name = %q, want %q", p.Name, tt.lookup)
			}
			if p.Mode != tt.wantMode {
				t.Errorf("Mode = %q, want %q", p.Mode, tt.wantMode)
			}
			if p.Body != tt.wantBody {
				t.Errorf("Body = %q, want %q", p.Body, tt.wantBody)
			}
			if p.Description != tt.wantDesc {
				t.Errorf("Description = %q, want %q", p.Description, tt.wantDesc)
			}
			if p.ReadOnly != tt.wantRO {
				t.Errorf("ReadOnly = %v, want %v", p.ReadOnly, tt.wantRO)
			}
		})
	}

	t.Run("built-in wins over shadowing rolefile", func(t *testing.T) {
		p, ok := ResolvePersona("build")
		if !ok || p.Body != "" || p.Description != builtinPersonas[BuildPersonaName].Description {
			t.Errorf("ResolvePersona(build) = %+v, ok %v; want the built-in persona", p, ok)
		}
	})
}
