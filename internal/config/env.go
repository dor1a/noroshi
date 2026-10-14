// Package config loads runtime settings from the environment and the page,
// component, and check definitions from a YAML file.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/dor1a/noroshi/internal/i18n"
)

// Env holds settings that differ between deployments. Every value comes from
// an environment variable; secrets may come from a file named by the same
// variable with a _FILE suffix, which takes precedence.
type Env struct {
	ConfigPath  string
	DatabaseURL string
	PublicAddr  string
	AdminAddr   string
	OpsAddr     string
	BaseURL     string
	AdminURL    string

	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCAdminGroup   string
	OIDCGroupsClaim  string
	OIDCScopes       []string
	SessionKey       []byte

	DefaultLocale string
	Theme         string
	DevNoAuth     bool
	DevEmail      string
	LogLevel      string
}

// LoadEnv reads and validates the environment.
func LoadEnv() (Env, error) {
	var errs []error
	get := func(name string) string {
		v, err := lookup(name)
		if err != nil {
			errs = append(errs, err)
		}
		return v
	}
	e := Env{
		ConfigPath:       or(get("NOROSHI_CONFIG"), "/etc/noroshi/config.yaml"),
		DatabaseURL:      get("NOROSHI_DATABASE_URL"),
		PublicAddr:       or(get("NOROSHI_PUBLIC_ADDR"), ":8080"),
		AdminAddr:        or(get("NOROSHI_ADMIN_ADDR"), ":8081"),
		OpsAddr:          or(get("NOROSHI_OPS_ADDR"), ":8082"),
		BaseURL:          strings.TrimRight(get("NOROSHI_BASE_URL"), "/"),
		AdminURL:         strings.TrimRight(get("NOROSHI_ADMIN_URL"), "/"),
		OIDCIssuer:       get("NOROSHI_OIDC_ISSUER"),
		OIDCClientID:     get("NOROSHI_OIDC_CLIENT_ID"),
		OIDCClientSecret: get("NOROSHI_OIDC_CLIENT_SECRET"),
		OIDCAdminGroup:   or(get("NOROSHI_OIDC_ADMIN_GROUP"), "noroshi-admins"),
		OIDCGroupsClaim:  or(get("NOROSHI_OIDC_GROUPS_CLAIM"), "groups"),
		OIDCScopes:       strings.Fields(or(get("NOROSHI_OIDC_SCOPES"), "openid profile email")),
		SessionKey:       []byte(get("NOROSHI_SESSION_KEY")),
		DefaultLocale:    or(i18n.Normalize(get("NOROSHI_DEFAULT_LOCALE")), "en"),
		LogLevel:         or(get("NOROSHI_LOG_LEVEL"), "info"),
		Theme:            or(get("NOROSHI_THEME"), "tokyonight"),
		DevEmail:         get("NOROSHI_DEV_EMAIL"),
	}
	if v := get("NOROSHI_DEV_NO_AUTH"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("NOROSHI_DEV_NO_AUTH: %w", err))
		}
		e.DevNoAuth = b
	}

	if e.DatabaseURL == "" {
		errs = append(errs, errors.New("NOROSHI_DATABASE_URL is required"))
	}
	if !e.DevNoAuth {
		for name, v := range map[string]string{
			"NOROSHI_ADMIN_URL":          e.AdminURL,
			"NOROSHI_OIDC_ISSUER":        e.OIDCIssuer,
			"NOROSHI_OIDC_CLIENT_ID":     e.OIDCClientID,
			"NOROSHI_OIDC_CLIENT_SECRET": e.OIDCClientSecret,
		} {
			if v == "" {
				errs = append(errs, fmt.Errorf("%s is required unless NOROSHI_DEV_NO_AUTH is set", name))
			}
		}
		if len(e.SessionKey) < 32 {
			errs = append(errs, errors.New("NOROSHI_SESSION_KEY must be at least 32 bytes"))
		}
		if e.AdminURL != "" {
			if u, err := url.Parse(e.AdminURL); err != nil || u.Scheme == "" || u.Host == "" {
				errs = append(errs, fmt.Errorf("NOROSHI_ADMIN_URL is not an absolute URL: %q", e.AdminURL))
			}
		}
	}
	return e, errors.Join(errs...)
}

// lookup reads NAME_FILE if set, otherwise NAME.
func lookup(name string) (string, error) {
	if path := os.Getenv(name + "_FILE"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("%s_FILE: %w", name, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	return os.Getenv(name), nil
}

func or(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
