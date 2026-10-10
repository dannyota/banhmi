package oauthserver

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestLoadConfig(t *testing.T) {
	primary := make([]byte, 32)
	rotated := make([]byte, 48)
	t.Setenv(oauthHMACKeyEnv, base64.RawStdEncoding.EncodeToString(primary))
	t.Setenv(oauthRotatedHMACKeysEnv, `["`+base64.RawStdEncoding.EncodeToString(rotated)+`"]`)
	origin, err := url.Parse("https://evidence.example")
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(origin)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.Issuer != origin.String() || cfg.Resource != "https://evidence.example/mcp" {
		t.Fatalf("LoadConfig() endpoints = %q, %q", cfg.Issuer, cfg.Resource)
	}
	if cfg.ConsentLifetime != 10*time.Minute {
		t.Fatalf("ConsentLifetime = %v", cfg.ConsentLifetime)
	}
	if len(cfg.GlobalSecret) != 32 || len(cfg.RotatedGlobalSecrets) != 1 || len(cfg.RotatedGlobalSecrets[0]) != 48 {
		t.Fatal("LoadConfig() did not decode HMAC keys")
	}
}

func TestLoadConfigErrorsDoNotExposeSecret(t *testing.T) {
	secret := "not-valid-base64-secret"
	t.Setenv(oauthHMACKeyEnv, secret)
	t.Setenv(oauthRotatedHMACKeysEnv, "[]")
	origin, err := url.Parse("https://evidence.example")
	if err != nil {
		t.Fatal(err)
	}

	_, err = LoadConfig(origin)
	if err == nil {
		t.Fatal("LoadConfig() error = nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("LoadConfig() error exposed a secret value")
	}
}

func TestConfigFormattingRedactsSecrets(t *testing.T) {
	cfg := Config{
		Issuer: "https://evidence.example", Resource: testResource,
		GlobalSecret: []byte{11, 22, 33, 44}, RotatedGlobalSecrets: [][]byte{{55, 66, 77, 88}},
	}
	formatted := fmt.Sprintf("%v %+v %#v", cfg, cfg, cfg)
	for _, secretDump := range []string{fmt.Sprint(cfg.GlobalSecret), fmt.Sprint(cfg.RotatedGlobalSecrets)} {
		if strings.Contains(formatted, secretDump) {
			t.Fatalf("formatted Config exposed secret bytes: %s", formatted)
		}
	}
}

func TestValidateServerConfigRejectsResourceOutsideIssuer(t *testing.T) {
	err := validateServerConfig(Config{
		Issuer:          "https://evidence.example",
		Resource:        "https://other.example/mcp",
		GlobalSecret:    make([]byte, 32),
		ConsentLifetime: 10 * time.Minute,
	})
	if err == nil {
		t.Fatal("validateServerConfig() error = nil")
	}
}
