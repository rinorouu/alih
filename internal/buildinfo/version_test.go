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

package buildinfo

import (
	"strings"
	"testing"
)

func TestResolvePrefersTheInjectedVersionOverTheBuildIdentity(t *testing.T) {
	original := Version
	t.Cleanup(func() { Version = original })

	Version = "1.2.3"
	if resolved := Resolve("9.9.9-rc.1"); resolved != "9.9.9-rc.1" {
		t.Fatalf("resolved = %q, want the injected version", resolved)
	}
	if resolved := Resolve("   "); resolved != "1.2.3" {
		t.Fatalf("resolved = %q, want the build identity", resolved)
	}
	Version = ""
	if resolved := Resolve(""); resolved != "dev" {
		t.Fatalf("resolved = %q, want dev for a build that stamped nothing", resolved)
	}
	if resolved := Resolve(""); strings.Contains(resolved, "0.0.1") {
		t.Fatal("a version nobody built was resolved")
	}
}

func TestResolveNeverReturnsUnsafeOrUnboundedText(t *testing.T) {
	original := Version
	t.Cleanup(func() { Version = original })
	Version = "dev"

	if resolved := Resolve("1.0.0\n\x07malicious"); resolved != "1.0.0malicious" {
		t.Fatalf("resolved = %q, want the control characters removed", resolved)
	}
	if resolved := Resolve(string([]byte{0xff, 0xfe}) + "2.0.0"); resolved != "2.0.0" {
		t.Fatalf("resolved = %q, want invalid encoding removed", resolved)
	}
	long := Resolve(strings.Repeat("v", maxVersionBytes*2))
	if len(long) != maxVersionBytes {
		t.Fatalf("resolved length = %d, want it bounded to %d", len(long), maxVersionBytes)
	}
}
