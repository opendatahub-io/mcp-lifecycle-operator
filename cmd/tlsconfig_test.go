package main

import (
	"crypto/tls"
	"testing"
)

type noopLogger struct{}

func (noopLogger) Info(_ string, _ ...any) {}

func TestParseTLSVersion(t *testing.T) {
	tests := []struct {
		input string
		want  uint16
	}{
		{"VersionTLS10", tls.VersionTLS10},
		{"VersionTLS11", tls.VersionTLS11},
		{"VersionTLS12", tls.VersionTLS12},
		{"VersionTLS13", tls.VersionTLS13},
		{"", 0},
		{"unknown", 0},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseTLSVersion(tt.input)
			if got != tt.want {
				t.Errorf("parseTLSVersion(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseCipherSuites(t *testing.T) {
	log := noopLogger{}

	t.Run("known IANA names", func(t *testing.T) {
		ids, names := parseCipherSuites("TLS_AES_128_GCM_SHA256,TLS_AES_256_GCM_SHA384", log)
		if len(ids) != 2 {
			t.Fatalf("expected 2 suites, got %d", len(ids))
		}
		if ids[0] != tls.TLS_AES_128_GCM_SHA256 {
			t.Errorf("ids[0] = %#x, want %#x", ids[0], tls.TLS_AES_128_GCM_SHA256)
		}
		if ids[1] != tls.TLS_AES_256_GCM_SHA384 {
			t.Errorf("ids[1] = %#x, want %#x", ids[1], tls.TLS_AES_256_GCM_SHA384)
		}
		if len(names) != 2 || names[0] != "TLS_AES_128_GCM_SHA256" || names[1] != "TLS_AES_256_GCM_SHA384" {
			t.Errorf("names = %v, want [TLS_AES_128_GCM_SHA256 TLS_AES_256_GCM_SHA384]", names)
		}
	})

	t.Run("empty input", func(t *testing.T) {
		ids, names := parseCipherSuites("", log)
		if ids != nil {
			t.Errorf("expected nil ids, got %v", ids)
		}
		if names != nil {
			t.Errorf("expected nil names, got %v", names)
		}
	})

	t.Run("unknown names are skipped", func(t *testing.T) {
		ids, names := parseCipherSuites("TLS_AES_256_GCM_SHA384,UNKNOWN_CIPHER", log)
		if len(ids) != 1 {
			t.Fatalf("expected 1 suite, got %d", len(ids))
		}
		if len(names) != 1 || names[0] != "TLS_AES_256_GCM_SHA384" {
			t.Errorf("names = %v, want [TLS_AES_256_GCM_SHA384]", names)
		}
	})

	t.Run("TLS 1.2 ECDHE cipher", func(t *testing.T) {
		ids, names := parseCipherSuites("TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256", log)
		if len(ids) != 1 {
			t.Fatalf("expected 1 suite, got %d", len(ids))
		}
		if ids[0] != tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256 {
			t.Errorf("ids[0] = %#x, want %#x", ids[0], tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256)
		}
		if names[0] != "TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256" {
			t.Errorf("names[0] = %s, want TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256", names[0])
		}
	})
}

func TestParseTLSSettings_Unset(t *testing.T) {
	t.Setenv(envTLSMinVersion, "")
	t.Setenv(envTLSCipherSuites, "")

	s := parseTLSSettings()
	if fn := s.tlsConfigFunc(); fn != nil {
		t.Error("expected nil tlsConfigFunc when env vars are unset")
	}
	if envVars := s.envVars(); envVars != nil {
		t.Errorf("expected nil envVars when env vars are unset, got %v", envVars)
	}
}

func TestParseTLSSettings_AppliesConfig(t *testing.T) {
	t.Setenv(envTLSMinVersion, "VersionTLS12")
	t.Setenv(envTLSCipherSuites, "TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256")

	s := parseTLSSettings()

	fn := s.tlsConfigFunc()
	if fn == nil {
		t.Fatal("expected non-nil TLS config function")
	}

	cfg := &tls.Config{}
	fn(cfg)

	if cfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %d, want %d", cfg.MinVersion, tls.VersionTLS12)
	}
	if len(cfg.CipherSuites) != 2 {
		t.Fatalf("expected 2 cipher suites, got %d", len(cfg.CipherSuites))
	}
}

func TestTLSSettings_EnvVars(t *testing.T) {
	t.Setenv(envTLSMinVersion, "")
	t.Setenv(envTLSCipherSuites, "")

	t.Run("returns both vars when both are valid", func(t *testing.T) {
		t.Setenv(envTLSMinVersion, "VersionTLS12")
		t.Setenv(envTLSCipherSuites, "TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384")

		s := parseTLSSettings()
		envVars := s.envVars()
		if len(envVars) != 2 {
			t.Fatalf("expected 2 env vars, got %d", len(envVars))
		}
		if envVars[0].Name != envTLSMinVersion || envVars[0].Value != "VersionTLS12" {
			t.Errorf("envVars[0] = %s=%s, want TLS_MIN_VERSION=VersionTLS12", envVars[0].Name, envVars[0].Value)
		}
		if envVars[1].Name != envTLSCipherSuites || envVars[1].Value != "TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384" {
			t.Errorf("envVars[1] = %s=%s, want TLS_CIPHER_SUITES=TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384", envVars[1].Name, envVars[1].Value)
		}
	})

	t.Run("returns only valid vars", func(t *testing.T) {
		t.Setenv(envTLSMinVersion, "VersionTLS13")

		s := parseTLSSettings()
		envVars := s.envVars()
		if len(envVars) != 1 {
			t.Fatalf("expected 1 env var, got %d", len(envVars))
		}
		if envVars[0].Name != envTLSMinVersion || envVars[0].Value != "VersionTLS13" {
			t.Errorf("envVars[0] = %s=%s, want TLS_MIN_VERSION=VersionTLS13", envVars[0].Name, envVars[0].Value)
		}
	})

	t.Run("returns nil when neither var is set", func(t *testing.T) {
		s := parseTLSSettings()
		envVars := s.envVars()
		if envVars != nil {
			t.Errorf("expected nil, got %v", envVars)
		}
	})

	t.Run("excludes invalid min version", func(t *testing.T) {
		t.Setenv(envTLSMinVersion, "garbage")

		s := parseTLSSettings()
		envVars := s.envVars()
		if envVars != nil {
			t.Errorf("expected nil for invalid min version, got %v", envVars)
		}
	})

	t.Run("excludes unknown cipher suites from value", func(t *testing.T) {
		t.Setenv(envTLSCipherSuites, "TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,UNKNOWN_CIPHER")

		s := parseTLSSettings()
		envVars := s.envVars()
		if len(envVars) != 1 {
			t.Fatalf("expected 1 env var, got %d", len(envVars))
		}
		if envVars[0].Value != "TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384" {
			t.Errorf("expected only valid cipher in value, got %s", envVars[0].Value)
		}
	})

	t.Run("excludes all-invalid cipher suites", func(t *testing.T) {
		t.Setenv(envTLSCipherSuites, "UNKNOWN_CIPHER,ALSO_UNKNOWN")

		s := parseTLSSettings()
		envVars := s.envVars()
		if envVars != nil {
			t.Errorf("expected nil when all cipher suites are invalid, got %v", envVars)
		}
	})
}

func TestParseTLSSettings_TLS13_SkipsCipherSuites(t *testing.T) {
	t.Setenv(envTLSMinVersion, "VersionTLS13")
	t.Setenv(envTLSCipherSuites, "TLS_AES_128_GCM_SHA256,TLS_AES_256_GCM_SHA384")

	s := parseTLSSettings()
	fn := s.tlsConfigFunc()
	if fn == nil {
		t.Fatal("expected non-nil TLS config function")
	}

	cfg := &tls.Config{}
	fn(cfg)

	if cfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion = %d, want %d", cfg.MinVersion, tls.VersionTLS13)
	}
	if cfg.CipherSuites != nil {
		t.Errorf("CipherSuites should be nil for TLS 1.3, got %v", cfg.CipherSuites)
	}
}
