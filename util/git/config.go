// Derived from Argo CD (https://github.com/argoproj/argo-cd/commit/559dc16e7216dc8e250eeb69f78f749396a74e9f),
// licensed under the Apache License, Version 2.0.
//
// Modifications Copyright 2026 Syntasso

package git

import "time"

type Config struct {
	TLSDataPath string `envconfig:"KRATIX_TLS_DATA_PATH"`

	// GithubAppCredsExpirationDuration controls the caching of Github app credentials.
	GithubAppCredsExpirationDuration time.Duration `envconfig:"KRATIX_GITHUB_APP_CREDS_EXPIRATION_DURATION" default:"60m"`

	Timeout      time.Duration `envconfig:"KRATIX_EXEC_TIMEOUT" default:"90s"`
	FatalTimeout time.Duration `envconfig:"KRATIX_EXEC_FATAL_TIMEOUT" default:"10s"`
}
