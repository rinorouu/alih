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

// Package compat holds no production code. It exists to pin the artifact
// formats Alih has already released or already written to a user's disk, so a
// later build cannot quietly stop reading them or quietly rewrite them.
//
// The archives under testdata were produced by the code at tag v0.2.4 rather
// than reconstructed by the current writers. That is the point: a fixture the
// current code generates would change shape whenever the current writer
// changes, and would therefore prove nothing about compatibility. These bytes
// are frozen and must not be regenerated to make a test pass. If a change makes
// them unreadable, either the change is wrong or it needs an explicit,
// documented migration.
package compat
