// SPDX-FileCopyrightText: 2026 wagaStrim contributors
// SPDX-License-Identifier: MIT

package config

import "errors"

var (
	// ErrLocateConfig is returned when the user configuration directory cannot be determined.
	ErrLocateConfig = errors.New("cannot locate user config directory")
	// ErrReadConfig is returned when an existing configuration file cannot be read.
	ErrReadConfig = errors.New("cannot read config")
	// ErrParseConfig is returned when the configuration file is not valid JSON.
	ErrParseConfig = errors.New("cannot parse config")
	// ErrWriteConfig is returned when the configuration file cannot be persisted.
	ErrWriteConfig = errors.New("cannot write config")
	// ErrGenerateKey is returned when the system random source fails.
	ErrGenerateKey = errors.New("cannot generate key")
	// ErrUnknownIngest is returned when no ingest matches the supplied identifier.
	ErrUnknownIngest = errors.New("unknown ingest")
	// ErrFutureConfig is returned when the file was written by a newer build.
	ErrFutureConfig = errors.New("config was written by a newer version")
)
