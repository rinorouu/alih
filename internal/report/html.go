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

package report

import (
	"html/template"
	"io"

	"alih/internal/verify"
)

// reportTemplate renders the same evidence as RenderText into a single
// self-contained file. It loads no external stylesheet, script or font, so the
// report stays readable from a local archive with no network access.
var reportTemplate = template.Must(template.New("report").Funcs(template.FuncMap{
	"statusClass": statusClass,
	"orUnknown":   orUnknown,
}).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Alih Recovery Report — {{orUnknown .Identity.WorkspaceName}}</title>
<style>
:root { color-scheme: light dark; }
body { font-family: ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, sans-serif;
       line-height: 1.55; margin: 0 auto; max-width: 62rem; padding: 2rem 1.25rem 4rem; }
h1 { font-size: 1.6rem; margin-bottom: .25rem; }
h2 { font-size: 1.1rem; margin-top: 2.5rem; border-bottom: 1px solid currentColor; padding-bottom: .3rem; }
p.sub { opacity: .75; margin-top: 0; }
table { border-collapse: collapse; width: 100%; margin: .5rem 0; }
th, td { text-align: left; padding: .35rem .6rem; border-bottom: 1px solid rgba(128,128,128,.35); vertical-align: top; }
th { font-weight: 600; }
td.num { text-align: right; font-variant-numeric: tabular-nums; }
code, .mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: .9em; word-break: break-all; }
.tag { display: inline-block; padding: .05rem .45rem; border-radius: .25rem; font-size: .78rem;
       font-weight: 600; letter-spacing: .02em; border: 1px solid currentColor; white-space: nowrap; }
.good { color: #1a7f37; } .bad { color: #b42318; } .warn { color: #9a6700; } .flat { opacity: .8; }
.result { font-size: 1.25rem; font-weight: 700; }
ul { padding-left: 1.15rem; } li { margin: .3rem 0; }
.finding { font-size: .92rem; opacity: .9; }
.verdict { border-left: 4px solid currentColor; padding: .35rem 0 .35rem .9rem; margin: .75rem 0; }
dl { display: grid; grid-template-columns: max-content 1fr; gap: .25rem 1.25rem; margin: .5rem 0; }
dt { font-weight: 600; } dd { margin: 0; }
footer { margin-top: 3rem; font-size: .85rem; opacity: .75; }
</style>
</head>
<body>
<h1>Alih — Recovery Report</h1>
<p class="sub">Generated {{.GeneratedAt.Format "2006-01-02 15:04:05 UTC"}} from archived evidence only. The source was not contacted.</p>

<h2>1. Archive identity</h2>
<dl>
<dt>Archive</dt><dd class="mono">{{.Identity.ArchivePath}}</dd>
<dt>Connector</dt><dd>{{orUnknown .Identity.Connector}}</dd>
<dt>Source workspace</dt><dd>{{orUnknown .Identity.WorkspaceName}} <span class="mono">(ID: {{orUnknown .Identity.WorkspaceID}})</span></dd>
<dt>Extracted by</dt><dd>{{if .Identity.ExtractedByID}}{{orUnknown .Identity.ExtractedByName}} <span class="mono">(ID: {{.Identity.ExtractedByID}})</span>{{else}}<span class="bad">not recorded: this archive does not name the account that produced it</span>{{end}}<br><span class="finding">This archive holds what that account could reach through the official API. Whether that is the entire Workspace is not established.</span></dd>
<dt>Source read completed</dt><dd>{{if .Identity.SourceSnapshotCompletedAt}}{{.Identity.SourceSnapshotCompletedAt.Format "2006-01-02 15:04:05 UTC"}}{{else}}not recorded by this archive{{end}}</dd>
<dt>Archive completed</dt><dd>{{if .Identity.ArchiveCompletedAt}}{{.Identity.ArchiveCompletedAt.Format "2006-01-02 15:04:05 UTC"}}{{if .Identity.CompletionLag}}<br><span class="finding">{{.Identity.CompletionLag}}</span>{{end}}{{else}}<span class="bad">not recorded: this archive states no completion time</span>{{end}}</dd>
<dt>Created by</dt><dd>Alih {{orUnknown .Identity.CreatedByAlihVersion}}</dd>
<dt>Recorded archive status</dt><dd>{{orUnknown .Identity.RecordedStatus}}</dd>
<dt>Source snapshot digest</dt><dd class="mono">{{orUnknown .Identity.SourceSnapshotDigest}}</dd>
<dt>Source snapshot</dt><dd>{{if .Identity.SourceSnapshotAtomic}}atomic{{else}}<span class="tag warn">NON-ATOMIC</span> records may reflect different moments of the extraction{{end}}</dd>
<dt>Files in manifest</dt><dd>{{.Identity.RecordedFiles}}</dd>
</dl>
{{if not .Identity.ManifestReadable}}<p class="bad"><strong>manifest.json could not be read:</strong> {{orUnknown .Identity.ManifestError}}. The archive does not state what it is.</p>{{end}}

<h2>2. Verification status</h2>
<p class="result"><span class="tag {{statusClass .Verification.Result}}">{{.Verification.Result}}</span></p>
<p>{{.Verification.Headline}}</p>
<p>{{.Verification.FiguresTrust}}</p>
<p class="finding">The capability states in section 6 describe the source. They are not a statement about this archive's integrity, which is what the checks below measure.</p>
<table>
<thead><tr><th>Check</th><th>Status</th><th>Detail</th></tr></thead>
<tbody>
{{range .Verification.Checks}}
<tr>
<td class="mono">{{.Name}}</td>
<td><span class="tag {{statusClass .Status}}">{{.Status}}</span></td>
<td>{{.Summary}}
{{if .Findings}}<ul class="finding">{{range .Findings}}<li>{{.}}</li>{{end}}</ul>{{end}}</td>
</tr>
{{end}}
</tbody>
</table>

<h2>3. Recovery summary</h2>
<p>What this archive supports, and what it does not, according to verification.</p>
<table>
<thead><tr><th>Claim</th><th>Established</th></tr></thead>
<tbody>
{{range .Recovery}}
<tr>
<td>{{.Claim}}{{if not .Proven}}<br><span class="finding">{{.Reason}}</span>{{end}}</td>
<td><span class="tag {{if .Proven}}good{{else}}bad{{end}}">{{if .Proven}}PROVEN{{else}}NOT PROVEN{{end}}</span></td>
</tr>
{{end}}
</tbody>
</table>

<h2>4. Entity coverage</h2>
{{if .Coverage}}
<table>
<thead><tr><th>Entity</th><th class="num">Expected</th><th class="num">Archived</th><th class="num">Unresolved</th><th>Status</th></tr></thead>
<tbody>
{{range .Coverage}}
<tr><td>{{.Entity}}</td><td class="num">{{.Expected}}</td><td class="num">{{.Archived}}</td><td class="num">{{.Unresolved}}</td>
<td><span class="tag {{statusClass .Status}}">{{.Status}}</span></td></tr>
{{end}}
</tbody>
</table>
{{else}}<p class="bad">No entity coverage could be established.</p>{{end}}

<h2>5. Attachments</h2>
<dl>
<dt>Expected</dt><dd>{{.Attachments.Expected}}</dd>
<dt>Preserved</dt><dd>{{.Attachments.Retrieved}}</dd>
<dt>Not preserved</dt><dd>{{.Attachments.UnresolvedCount}}</dd>
<dt>Integrity check</dt><dd><span class="tag {{statusClass .Attachments.IntegrityCheck}}">{{.Attachments.IntegrityCheck}}</span></dd>
</dl>
<p>{{.Attachments.IntegrityNote}}</p>
{{if .Attachments.Unresolved}}
<p><strong>Expected but not archived:</strong></p>
<table>
<thead><tr><th>Source ID</th><th>Filename</th><th>Reason</th></tr></thead>
<tbody>
{{range .Attachments.Unresolved}}
<tr><td class="mono">{{.SourceID}}</td><td>{{if .Filename}}{{.Filename}}{{else}}<span class="flat">no filename recorded</span>{{end}}</td><td>{{.Reason}}</td></tr>
{{end}}
</tbody>
</table>
{{end}}

<h2>6. Capability coverage</h2>
{{if .Capabilities}}
<table>
<thead><tr><th>Capability</th><th>Source state</th><th>What this means for recovery</th><th>This archive</th></tr></thead>
<tbody>
{{range .Capabilities}}
<tr><td>{{.Name}}</td>
<td><span class="tag {{statusClass .State}}">{{.State}}</span></td>
<td>{{.RecoveryMeaning}}{{if .Note}}<br><span class="finding">Source note: {{.Note}}</span>{{end}}</td>
<td class="finding">{{.ArchiveEvidence}}</td></tr>
{{end}}
</tbody>
</table>
{{else}}<p class="bad">The archive declares no source capabilities, so its scope is not established.</p>{{end}}

<h2>7. Limitations and unproven claims</h2>
{{if .Limitations}}<ul>{{range .Limitations}}<li>{{.}}</li>{{end}}</ul>{{else}}<p>None recorded.</p>{{end}}

<h2>8. Discrepancies and unresolved items</h2>
{{if .Discrepancies}}
<table>
<thead><tr><th>Kind</th><th>Source ID</th><th>Detail</th><th>Origin</th></tr></thead>
<tbody>
{{range .Discrepancies}}
<tr><td class="mono">{{.Kind}}</td><td class="mono">{{if .SourceID}}{{.SourceID}}{{else}}—{{end}}</td><td>{{.Message}}</td><td class="flat">{{.Origin}}</td></tr>
{{end}}
</tbody>
</table>
{{else}}<p>None. Neither the archive nor verification recorded an unresolved item.</p>{{end}}

<h2>9. Recovery conclusion</h2>
<p class="result"><span class="tag {{statusClass .Conclusion.Result}}">{{.Conclusion.Result}}</span></p>
<div class="verdict"><strong>{{.Conclusion.Verdict}}</strong></div>
<ul>{{range .Conclusion.Statements}}<li>{{.}}</li>{{end}}</ul>
<p><strong>What must NOT be claimed from this archive:</strong></p>
<ul>{{range .MustNotClaim}}<li>{{.}}</li>{{end}}</ul>

<footer>
Alih {{orUnknown .AlihVersion}} recovery report, schema {{.SchemaVersion}}.
Produced from the archive's own evidence and its M5 verification result.
No source data modified. No archive data modified.
</footer>
</body>
</html>
`))

// RenderHTML writes the recovery report as a single self-contained HTML file.
func RenderHTML(output io.Writer, document Document) error {
	return reportTemplate.Execute(output, document)
}

// statusClass maps a verification or capability state to a presentation class.
// A state it does not recognise is never styled as good.
func statusClass(state string) string {
	switch state {
	case verify.CheckPass, verify.ResultVerified, "SUPPORTED":
		return "good"
	case verify.ResultVerifiedWithLimitations, verify.CheckUnproven, verify.CheckIncomplete, "PARTIAL", "UNKNOWN":
		// CheckIncomplete and ResultIncomplete share the INCOMPLETE value.
		return "warn"
	case verify.CheckFail, verify.CheckNotEvaluated, "UNSUPPORTED", "UNAVAILABLE":
		// CheckFail and ResultFailed share the FAILED value.
		return "bad"
	default:
		return "flat"
	}
}
