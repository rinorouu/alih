// Copyright 2025 rinorouu
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package buildinfo contains identity supplied by the build process.
package buildinfo

import (
	"strings"
	"unicode/utf8"
)

// Version is replaced for release builds with:
//
//	-ldflags "-X alih/internal/buildinfo.Version=<version>"
var Version = "dev"

// maxVersionBytes bounds what a build can stamp into recorded provenance.
const maxVersionBytes = 64

// Resolve returns the version an artifact should record. A caller that was
// given a version uses it; otherwise the running build's own identity is used,
// so no artifact can ever record a version nobody built. The result is always
// safe to write into JSON, a database, or terminal output.
func Resolve(injected string) string {
	if version := sanitize(injected); version != "" {
		return version
	}
	if version := sanitize(Version); version != "" {
		return version
	}
	return "dev"
}

func sanitize(value string) string {
	cleaned := strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f {
			return -1
		}
		return character
	}, strings.ToValidUTF8(strings.TrimSpace(value), ""))
	if len(cleaned) > maxVersionBytes {
		cleaned = cleaned[:maxVersionBytes]
		for len(cleaned) > 0 && !utf8.ValidString(cleaned) {
			cleaned = cleaned[:len(cleaned)-1]
		}
	}
	return strings.TrimSpace(cleaned)
}
