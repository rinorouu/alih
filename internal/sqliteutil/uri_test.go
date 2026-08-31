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

package sqliteutil

import "testing"

func TestFileURI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		goos string
		path string
		want string
	}{
		{name: "Unix absolute path", goos: "linux", path: "/tmp/alih archive/alih.db", want: "file:///tmp/alih%20archive/alih.db"},
		{name: "Windows drive path", goos: "windows", path: `C:\Users\Alih User\alih.db`, want: "file:///C:/Users/Alih%20User/alih.db"},
		{name: "Windows UNC path", goos: "windows", path: `\\server\backups\alih.db`, want: "file://server/backups/alih.db"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := fileURI(test.goos, test.path); got != test.want {
				t.Fatalf("fileURI(%q, %q) = %q, want %q", test.goos, test.path, got, test.want)
			}
		})
	}
}
