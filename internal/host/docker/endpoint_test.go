package docker

import "testing"

func TestResolvePrecedence(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		env        Env
		want       string
		wantOrigin Origin
	}{
		{
			name:       "configured wins over everything",
			configured: "tcp://10.0.0.5:2375",
			env:        Env{Garrison: "unix:///g.sock", Docker: "unix:///d.sock", GOOS: "windows"},
			want:       "tcp://10.0.0.5:2375",
			wantOrigin: OriginConfig,
		},
		{
			name:       "GARRISON_DOCKER_HOST outranks DOCKER_HOST",
			env:        Env{Garrison: "unix:///g.sock", Docker: "unix:///d.sock"},
			want:       "unix:///g.sock",
			wantOrigin: OriginGarEnv,
		},
		{
			name:       "DOCKER_HOST is honoured, so a WSL shell needs no telling twice",
			env:        Env{Docker: "unix:///run/user/1000/docker.sock"},
			want:       "unix:///run/user/1000/docker.sock",
			wantOrigin: OriginDockEnv,
		},
		{
			name:       "windows defaults to the named pipe",
			env:        Env{GOOS: "windows"},
			want:       WindowsEndpoint,
			wantOrigin: OriginDefault,
		},
		{
			name:       "linux defaults to the unix socket",
			env:        Env{GOOS: "linux"},
			want:       UnixEndpoint,
			wantOrigin: OriginDefault,
		},
		{
			name:       "blank configured value falls through rather than erroring",
			configured: "   ",
			env:        Env{GOOS: "linux"},
			want:       UnixEndpoint,
			wantOrigin: OriginDefault,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, origin, err := Resolve(tt.configured, tt.env)
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("endpoint = %q, want %q", got, tt.want)
			}
			if origin != tt.wantOrigin {
				t.Errorf("origin = %q, want %q", origin, tt.wantOrigin)
			}
		})
	}
}

// A config file is a place people type paths, not URLs. Every form below is
// something a reasonable person would write and expect to work.
func TestResolveNormalizesBarePaths(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{`\\.\pipe\docker_engine`, "npipe:////./pipe/docker_engine"},
		{"//./pipe/docker_engine", "npipe:////./pipe/docker_engine"},
		{"/var/run/docker.sock", "unix:///var/run/docker.sock"},
		{"localhost:2375", "tcp://localhost:2375"},
		{"unix:///var/run/docker.sock", "unix:///var/run/docker.sock"},
		{"npipe:////./pipe/docker_engine", "npipe:////./pipe/docker_engine"},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, _, err := Resolve(tt.in, Env{})
			if err != nil {
				t.Fatalf("Resolve(%q) error = %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("Resolve(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestResolveRejectsWhatItCannotDial(t *testing.T) {
	for _, in := range []string{"ftp://host/x", "docker_engine", "://nohost"} {
		if got, _, err := Resolve(in, Env{}); err == nil {
			t.Errorf("Resolve(%q) = %q, want an error", in, got)
		}
	}
}

func TestTransport(t *testing.T) {
	if got := Transport(WindowsEndpoint); got != "npipe" {
		t.Errorf("Transport(windows) = %q, want npipe", got)
	}
	if got := Transport(UnixEndpoint); got != "unix" {
		t.Errorf("Transport(unix) = %q, want unix", got)
	}
}
