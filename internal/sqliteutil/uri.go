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

// Package sqliteutil contains the path handling shared by SQLite readers and
// writers. SQLite file URIs need special treatment for Windows drive letters
// and UNC paths; net/url alone otherwise treats a drive letter as an authority.
package sqliteutil

import (
	"net/url"
	"runtime"
	"strings"
)

// FileURI returns a properly escaped SQLite file URI for a native filesystem
// path on the current platform.
func FileURI(path string) string {
	return fileURI(runtime.GOOS, path)
}

func fileURI(goos, path string) string {
	if goos != "windows" {
		return (&url.URL{Scheme: "file", Path: path}).String()
	}

	normalized := strings.ReplaceAll(path, `\`, "/")
	if strings.HasPrefix(normalized, "//") {
		serverAndPath := strings.TrimPrefix(normalized, "//")
		server, sharePath, found := strings.Cut(serverAndPath, "/")
		if found && server != "" {
			return (&url.URL{Scheme: "file", Host: server, Path: "/" + sharePath}).String()
		}
	}
	if len(normalized) >= 2 && normalized[1] == ':' {
		normalized = "/" + normalized
	}
	return (&url.URL{Scheme: "file", Path: normalized}).String()
}
