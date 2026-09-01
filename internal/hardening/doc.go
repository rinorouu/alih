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

// Package hardening holds no production code. It keeps HARDENING.md honest:
// the traceability matrix claims that each operational failure scenario is
// covered by named automated tests, and the test in this package checks that
// every name it cites still exists. Without it the matrix would be prose that
// quietly stops being true the first time a test is renamed or deleted.
package hardening
