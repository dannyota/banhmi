package identity

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const googleIssuer = "https://accounts.google.com"

type providerClaims struct {
	Issuer        string
	Subject       string
	Nonce         string
	Email         string
	EmailVerified bool
	DisplayName   string
}

type loginProvider interface {
	AuthorizationURL(state, nonce, challenge string) string
	Exchange(context.Context, string, string) (string, error)
	Verify(context.Context, string) (providerClaims, error)
}

type googleProvider struct {
	oauth    oauth2.Config
	verifier *oidc.IDTokenVerifier
}

func discoverGoogle(ctx context.Context, cfg Config) (loginProvider, error) {
	client := &http.Client{Timeout: providerTimeout}
	providerContext := oidc.ClientContext(ctx, client)
	provider, err := oidc.NewProvider(providerContext, googleIssuer)
	if err != nil {
		return nil, fmt.Errorf("discover Google OpenID provider: %w", err)
	}
	redirect := cfg.PublicOrigin.ResolveReference(&url.URL{Path: "/auth/google/callback"}).String()
	return &googleProvider{
		oauth: oauth2.Config{
			ClientID:     cfg.GoogleClientID,
			ClientSecret: cfg.GoogleClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  redirect,
			Scopes:       []string{oidc.ScopeOpenID, oidc.ScopeEmail, oidc.ScopeProfile},
		},
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.GoogleClientID}),
	}, nil
}

func (p *googleProvider) AuthorizationURL(state, nonce, challenge string) string {
	return p.oauth.AuthCodeURL(
		state,
		oidc.Nonce(nonce),
		oauth2.SetAuthURLParam("code_challenge", challenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
}

func (p *googleProvider) Exchange(ctx context.Context, code, verifier string) (string, error) {
	token, err := p.oauth.Exchange(ctx, code, oauth2.SetAuthURLParam("code_verifier", verifier))
	if err != nil {
		return "", fmt.Errorf("exchange Google authorization code: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return "", fmt.Errorf("Google token response has no ID token")
	}
	return rawIDToken, nil
}

func (p *googleProvider) Verify(ctx context.Context, rawIDToken string) (providerClaims, error) {
	idToken, err := p.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return providerClaims{}, fmt.Errorf("verify Google ID token: %w", err)
	}
	var values struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
	}
	if err := idToken.Claims(&values); err != nil {
		return providerClaims{}, fmt.Errorf("decode Google ID token claims: %w", err)
	}
	if idToken.Subject == "" {
		return providerClaims{}, fmt.Errorf("Google ID token has invalid identity claims")
	}
	return providerClaims{
		Issuer:        googleIssuer,
		Subject:       idToken.Subject,
		Nonce:         idToken.Nonce,
		Email:         values.Email,
		EmailVerified: values.EmailVerified,
		DisplayName:   values.Name,
	}, nil
}
