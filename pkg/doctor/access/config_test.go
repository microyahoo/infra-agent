package access

import (
	"testing"
	"time"
)

func TestConfigDefaults(t *testing.T) {
	c := Config{Host: "10.20.30.1"}
	c.applyDefaults()
	if c.Port != 22 {
		t.Errorf("Port = %d, want 22", c.Port)
	}
	if c.User != "root" {
		t.Errorf("User = %q, want root", c.User)
	}
	if c.CommandTimeout != 30*time.Second {
		t.Errorf("CommandTimeout = %v", c.CommandTimeout)
	}
	if c.MaxOutputBytes != 262144 {
		t.Errorf("MaxOutputBytes = %d", c.MaxOutputBytes)
	}
	if c.MaxConcurrency != 8 {
		t.Errorf("MaxConcurrency = %d", c.MaxConcurrency)
	}
	if c.Policy == nil {
		t.Error("Policy should default to DefaultPolicy()")
	}
}

func TestConfigValidateRequiresHost(t *testing.T) {
	c := Config{PrivateKeyPath: "/tmp/k"}
	c.applyDefaults()
	if err := c.validate(); err == nil {
		t.Fatal("expected error for missing host")
	}
}

func TestConfigValidateRequiresAuth(t *testing.T) {
	c := Config{Host: "10.0.0.1"}
	c.applyDefaults()
	if err := c.validate(); err == nil {
		t.Fatal("expected error when neither key nor password provided")
	}
}

func TestConfigValidateAcceptsPrivateKey(t *testing.T) {
	c := Config{Host: "10.0.0.1", PrivateKeyPath: "/tmp/k"}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}
