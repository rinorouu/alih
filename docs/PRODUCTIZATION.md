Alih — Productization

Alpha v0.1

Status

Phase: Alpha
Scope: ClickUp
Primary Interface: CLI
Product Language: English
Distribution: Compiled binary
Source Code: Private

Purpose

Membawa core Alih M0–M7 dari technical prototype menjadi utility yang dapat digunakan oleh external tester tanpa memahami pipeline internal Alih.

Alpha v0.1 tidak bertujuan menambah kemampuan fundamental baru pada core Alih.

Fokus fase ini adalah membuat kemampuan yang sudah terbukti dapat digunakan oleh orang lain secara sederhana, aman, dan tetap mempertahankan prinsip evidence-backed serta fail-closed yang sudah dibangun pada M0–M7.

---

1. Goal

Orang lain harus dapat menjalankan backup ClickUp menggunakan Alih tanpa perlu memahami proses internal:

- inventory scanning;
- raw extraction;
- portable archive creation;
- independent verification;
- recovery reporting.

Primary workflow harus sesederhana:

alih auth
alih backup

User tidak perlu mengetahui bagaimana M0–M7 bekerja untuk mendapatkan archive yang telah diverifikasi.

Alpha v0.1 harus membuktikan bahwa workflow Alih dapat digunakan di luar development environment utama tanpa melemahkan integrity guarantees dari core Alih.

---

2. Product Language

Semua output yang dilihat oleh user pada Alpha v0.1 menggunakan English.

Ini mencakup:

- CLI help;
- command descriptions;
- progress messages;
- success messages;
- errors;
- warnings;
- verification results;
- Recovery Reports;
- human-readable archive metadata;
- installation instructions;
- distribution instructions.

Contoh:

ALIH — BACKUP COMPLETE

Workspace: Example Workspace
Status: VERIFIED_WITH_LIMITATIONS

Archive:
/home/user/Alih/Demeter-API/2026-08-30T123000/

Recovery report:
/home/user/Alih/Demeter-API/2026-08-30T123000/recovery-report.html

Your ClickUp data was not modified.

Internal development documentation boleh menggunakan bahasa selain English.

Localization/i18n berada di luar scope Alpha v0.1.

Jangan membuat abstraction localization hanya untuk mengantisipasi kebutuhan yang belum terbukti.

---

3. Primary User Interface

Primary interface Alpha v0.1 adalah CLI.

User-facing workflow:

alih auth
alih backup

Advanced commands yang sudah ada tetap tersedia:

alih auth
alih scan
alih extract
alih export
alih verify
alih report

Advanced commands tersebut tetap berguna untuk:

- debugging;
- development;
- inspection;
- power users;
- troubleshooting.

Namun external Alpha tester tidak seharusnya diwajibkan memahami atau menjalankan pipeline tersebut secara manual.

---

4. Authentication

User melakukan authentication melalui:

alih auth

Alpha v0.1 menggunakan authentication mechanism lokal yang sudah tersedia pada core Alih.

Credential:

- berasal dari user;
- tidak boleh ditanam ke binary;
- tidak boleh dimasukkan ke portable archive;
- tidak boleh muncul pada log;
- tidak boleh muncul pada Recovery Report;
- tidak boleh muncul pada error message;
- tidak boleh bocor melalui redirect atau attachment retrieval;
- harus tetap mengikuti credential protection yang sudah diterapkan core Alih.

Authentication failure harus menghasilkan error yang jelas dan exit code non-zero.

Production OAuth berada di luar scope Alpha v0.1.

---

5. Backup Command

Primary operation:

alih backup

"alih backup" adalah orchestration layer, bukan engine baru.

Command tersebut menggunakan implementation M0–M7 yang sudah ada.

Secara konseptual:

scan
  ↓
extract
  ↓
export
  ↓
verify
  ↓
report

Orchestration layer tidak boleh membuat implementasi alternatif untuk:

- extraction;
- archive construction;
- verification;
- reporting;
- attachment handling;
- source traversal.

Core existing tetap menjadi source of truth.

---

6. Backup Pipeline

Ketika user menjalankan:

alih backup

Alih harus:

1. memastikan authentication tersedia;
2. menentukan Workspace yang akan diproses;
3. melakukan inventory/scan sesuai core existing;
4. membuat raw extraction;
5. membangun portable archive;
6. melakukan independent verification terhadap archive;
7. menghasilkan Recovery Report;
8. hanya menyatakan backup berhasil apabila verification mengizinkannya;
9. menampilkan lokasi final archive dan Recovery Report.

Setiap tahap hanya boleh dilanjutkan apabila prerequisite tahap sebelumnya terpenuhi.

Alih harus tetap fail-closed.

---

7. Workspace Selection

Jika authenticated account hanya memiliki satu Workspace yang dapat digunakan, Alih boleh menggunakan Workspace tersebut secara langsung selama behavior tersebut konsisten dan tidak ambigu.

Jika terdapat lebih dari satu Workspace, Alih tidak boleh diam-diam memilih Workspace yang arbitrary.

Workspace selection harus:

- deterministic;
- jelas bagi user;
- tidak menyebabkan backup Workspace yang salah.

UX final untuk multiple Workspace dapat disesuaikan selama tidak memperluas scope Alpha secara tidak perlu.

Existing "--workspace-id" semantics dapat digunakan kembali apabila sesuai.

---

8. Default Output Location

Default output:

~/Alih/<workspace>/<timestamp>/

Contoh:

~/Alih/Demeter-API/2026-08-30T123000/

Workspace component pada filesystem harus dibuat aman untuk digunakan sebagai path.

Timestamp harus memiliki semantics yang jelas dan tidak membuat klaim provenance yang ambigu.

Output location tidak boleh diam-diam menimpa archive existing.

---

9. Portable Archive

Successful backup menghasilkan portable archive berdasarkan format yang sudah dibangun oleh core Alih.

Minimal:

alih.db
attachments/
raw/
manifest.json
schema.json
recovery-report.html

File tambahan diperbolehkan apabila merupakan:

- archived evidence;
- metadata;
- verification/report artifact;
- bagian dari existing archive contract.

Productization tidak boleh mengubah format archive secara fundamental tanpa alasan yang berasal dari requirement nyata.

---

10. Raw Evidence

Raw evidence tetap menjadi bagian penting dari portable archive.

Alpha v0.1 tidak boleh menghilangkan raw evidence hanya untuk membuat archive terlihat lebih sederhana.

Raw evidence harus tetap memungkinkan:

- provenance inspection;
- verification;
- traceability;
- debugging;
- reconciliation terhadap portable representation.

Existing integrity checks terhadap raw evidence tetap berlaku.

---

11. Attachments

Attachments tetap mengikuti behavior core Alih.

Successful backup tidak boleh mengklaim attachment preservation apabila evidence tidak mendukung klaim tersebut.

Alih harus mempertahankan:

- attachment metadata;
- archived binary ketika retrieval berhasil;
- expected size ketika tersedia;
- archived size;
- checksum;
- source identity/reference;
- failure state apabila retrieval tidak berhasil.

Attachment corruption harus dapat menyebabkan verification failure sesuai existing verifier semantics.

---

12. Verification

Backup tidak boleh dianggap berhasil hanya karena extraction atau export selesai.

Portable archive harus melewati independent verification.

Status yang diperbolehkan sebagai successful Alpha backup:

VERIFIED
VERIFIED_WITH_LIMITATIONS

"VERIFIED_WITH_LIMITATIONS" tetap merupakan successful backup hanya dalam supported scope yang berhasil dibuktikan.

Seluruh limitation harus tetap ditampilkan.

Status seperti:

INCOMPLETE
FAILED

tidak boleh ditampilkan sebagai successful backup.

Verification failure tidak boleh diturunkan menjadi warning.

---

13. Verification Independence

Productization tidak boleh melemahkan independence dari M5 verifier.

Verification:

- membaca archive;
- tidak bergantung pada source ClickUp untuk membuktikan internal archive integrity;
- tidak memperbaiki archive agar verification lolos;
- tidak memodifikasi source;
- tidak memodifikasi evidence yang sedang diverifikasi.

"alih backup" tidak boleh membuat shortcut yang melewati verifier.

---

14. Recovery Report

Setiap successful backup menghasilkan human-readable Recovery Report.

Minimal:

recovery-report.html

Report harus berasal dari archived evidence dan verification result.

Recovery Report harus menggunakan English.

Report tidak boleh membuat klaim yang lebih kuat daripada verifier.

Report harus tetap membedakan:

- PROVEN evidence;
- FAILED evidence;
- NOT PROVEN evidence;
- source limitations;
- supported capabilities;
- PARTIAL capabilities;
- UNKNOWN capabilities;
- non-atomic source snapshot;
- discrepancies;
- unresolved items.

---

15. Provenance

Timestamp dan provenance harus menggunakan semantics yang sudah diperbaiki pada archive schema saat ini.

Alih harus membedakan event seperti:

- source snapshot/read completion;
- archive completion.

Jangan kembali menggunakan timestamp ambigu yang dapat ditafsirkan sebagai dua kejadian berbeda.

Report dan metadata harus menggunakan label yang sesuai dengan event yang benar-benar dibuktikan.

Filesystem modification time tidak boleh digunakan sebagai provenance authority kecuali secara eksplisit menjadi bagian dari archive contract di masa depan.

---

16. Non-Atomic Source

ClickUp tidak menyediakan atomic transaction snapshot lintas endpoint untuk workflow Alih saat ini.

Karena itu Alih tidak boleh menyatakan archive sebagai point-in-time consistent snapshot apabila evidence hanya menunjukkan:

atomic: false

Successful backup tetap dapat menghasilkan:

VERIFIED_WITH_LIMITATIONS

apabila internal archive integrity terbukti tetapi source limitation tersebut tetap ada.

Productization tidak boleh menyembunyikan limitation ini demi UX yang terlihat lebih sederhana.

---

17. Capability Preservation

Capability state yang sudah ditemukan core Alih harus tetap dipertahankan.

Contoh:

SUPPORTED
PARTIAL
UNKNOWN

Productization tidak boleh:

- mengubah UNKNOWN menjadi SUPPORTED;
- mengubah UNKNOWN menjadi unsupported tanpa evidence;
- mengubah PARTIAL menjadi SUPPORTED;
- menganggap absence dari archive berarti absence dari source.

Source capability state juga harus dibedakan dari integrity archive instance.

Contoh:

Task attachments: SUPPORTED

berarti Alih mendukung capability tersebut dalam source scope.

Ini tidak otomatis berarti attachment integrity pada setiap archive selalu berhasil.

Actual archive integrity tetap ditentukan verifier.

---

18. Custom Fields

Custom Field definitions dan observed values tetap mengikuti portable semantics core Alih.

Alih boleh mempertahankan observed value berdasarkan archived source definition.

Alih tidak boleh mengklaim executable semantics untuk:

- formulas;
- rollups;
- computed fields;
- source-side behavior lain yang tidak dapat direkonstruksi dari evidence.

Alpha v0.1 tidak menambahkan engine untuk mengeksekusi semantics ClickUp.

---

19. Successful Backup Contract

Backup dianggap berhasil hanya apabila:

1. required pipeline stages selesai;
2. portable archive berhasil dibuat;
3. archive dapat diverifikasi;
4. verification menghasilkan "VERIFIED" atau "VERIFIED_WITH_LIMITATIONS";
5. Recovery Report berhasil dibuat;
6. final archive location tersedia;
7. source ClickUp tidak dimodifikasi.

Successful execution harus menghasilkan exit code:

0

Contoh user-facing output:

ALIH — BACKUP COMPLETE

Workspace: Example Workspace
Status: VERIFIED_WITH_LIMITATIONS

Archive:
/home/user/Alih/Demeter-API/2026-08-30T123000/

Recovery report:
/home/user/Alih/Demeter-API/2026-08-30T123000/recovery-report.html

Your ClickUp data was not modified.

Wording final boleh berbeda selama tidak membuat verification claim yang lebih kuat.

---

20. Failure Contract

Jika workflow gagal:

- exit code harus non-zero;
- tahap yang gagal harus dapat diketahui;
- error harus menggunakan English;
- Alih tidak boleh menyatakan backup berhasil;
- verification failure tidak boleh diturunkan menjadi warning;
- incomplete archive tidak boleh terlihat seperti successful archive;
- corrupted archive tidak boleh terlihat seperti successful archive;
- source tidak boleh dimodifikasi untuk memperbaiki failure;
- evidence tidak boleh diam-diam diubah agar verification lolos.

Jika partial/failed working directory dipertahankan untuk forensic/debugging, statusnya harus jelas dan tidak boleh terlihat seperti completed backup.

---

21. Interruption

Behavior interruption harus tetap mengikuti hardening M7.

SIGINT/SIGTERM atau interruption lain yang dapat ditangani tidak boleh menghasilkan archive yang terlihat complete apabila pipeline belum selesai.

Partial work harus:

- dapat dibedakan dari completed archive;
- tidak menghasilkan false success;
- tidak menghasilkan false VERIFIED.

Full resumability tidak menjadi requirement Alpha v0.1.

---

22. Retry and Source Failure

Existing M7 hardening terhadap:

- HTTP 429;
- transient HTTP 5xx;
- timeout;
- retry exhaustion;
- pagination failure;
- oversized responses;
- attachment retrieval failure;

harus tetap digunakan.

"alih backup" tidak boleh membuat retry mechanism baru yang berbeda dari core tanpa alasan kuat.

Retry exhaustion harus menghasilkan failure yang jelas apabila required evidence tidak dapat diperoleh.

---

23. Source Safety

Alpha v0.1 tetap read-only terhadap ClickUp.

Alih tidak boleh:

- membuat task;
- mengubah task;
- menghapus task;
- membuat Space;
- mengubah Space;
- mengubah Folder;
- mengubah List;
- mengubah Custom Field;
- mengubah comment;
- mengubah attachment;
- mengubah relationship;
- mengubah permission;
- melakukan source-side repair;
- melakukan source-side migration.

Backup adalah operasi:

observe → preserve → verify → report

bukan:

modify → synchronize → migrate

---

24. CLI Exit Codes

CLI exit behavior harus deterministic.

Successful backup:

exit 0

Failed/incomplete backup:

non-zero

CLI orchestration tidak boleh menangkap failure dari underlying stage kemudian mengembalikan exit "0".

Existing M7 exit-code correctness harus dipertahankan.

---

25. Progress Output

"alih backup" boleh memberikan progress information kepada user.

Contoh:

ALIH — CLICKUP BACKUP

Workspace: Example Workspace

Scanning workspace...
Extracting source data...
Archiving attachments...
Building portable archive...
Verifying archive...
Generating recovery report...

Backup complete.

Progress output:

- harus menggunakan English;
- tidak boleh membocorkan credential;
- tidak boleh membuat success claim sebelum verification selesai;
- tidak boleh mengubah underlying verification semantics.

Detailed progress bar, percentage estimation, ETA, atau rich terminal UI tidak diperlukan untuk Alpha v0.1.

---

26. Advanced CLI

Existing commands tetap tersedia:

alih auth
alih scan
alih extract
alih export
alih verify
alih report

Command tersebut tetap menjadi useful engineering/power-user interface.

Primary external Alpha workflow:

alih auth
alih backup

Productization tidak boleh menghapus advanced commands hanya untuk menyederhanakan interface.

---

27. Distribution

Alpha v0.1 didistribusikan sebagai compiled binary.

Tester tidak membutuhkan:

- source repository;
- access ke private GitHub repository;
- Go toolchain;
- "git clone";
- "go build";
- development environment Alih.

Source code Alih tetap private.

Binary dapat didistribusikan secara langsung kepada closed Alpha tester.

---

28. Platform Support

Platform hanya didukung berdasarkan kebutuhan nyata Alpha tester.

Potential targets:

Linux amd64
Linux arm64
Windows amd64
macOS arm64
macOS amd64

Tidak ada requirement bahwa seluruh target tersebut harus tersedia sebelum closed Alpha dimulai.

Platform pertama sebaiknya mengikuti environment tester pertama.

Cross-platform support tidak boleh memperlambat validasi core workflow tanpa evidence kebutuhan.

---

29. Binary Security Expectations

Compiled binary tidak dianggap sebagai mekanisme perlindungan source code absolut.

Source repository tetap private.

Binary tidak boleh mengandung:

- developer ClickUp token;
- tester credential;
- private environment configuration;
- secret signing material;
- credential fixture nyata.

Build artifact harus berasal dari source yang diketahui dan tidak bergantung pada developer-local secret.

Advanced obfuscation berada di luar scope Alpha v0.1.

---

30. Versioning

Alpha binary harus memiliki version identity yang dapat ditampilkan kepada user atau digunakan saat troubleshooting.

Contoh:

alih 0.1.0-alpha

Exact versioning scheme dapat mengikuti existing project convention.

Tujuannya agar bug report dari tester dapat dikaitkan dengan build tertentu.

Productization tidak perlu membuat complex release infrastructure sebelum closed Alpha membutuhkan hal tersebut.

---

31. Logging and Diagnostics

Diagnostic information harus cukup untuk mengetahui tahap failure tanpa membocorkan sensitive information.

Logs/errors tidak boleh mengandung credential.

Alpha tester harus dapat memberikan informasi seperti:

Alih version
command
failed stage
error category
archive/report path when available

tanpa perlu memberikan ClickUp token.

Verbose/debug mode baru hanya boleh ditambahkan apabila benar-benar diperlukan dan tidak memperluas scope secara berlebihan.

---

32. Archive Privacy

Portable archive dapat berisi data Workspace user.

Karena itu Alpha v0.1 harus memperlakukan archive sebagai user-owned local data.

Default behavior:

- archive disimpan lokal;
- Alih tidak meng-upload archive ke server Alih;
- Alih tidak mengirim task content ke backend Alih;
- Alih tidak membutuhkan cloud service Alih untuk melakukan verification;
- Recovery Report dibuat lokal.

Cloud storage berada di luar scope.

---

33. Telemetry

Alpha v0.1 tidak membutuhkan mandatory telemetry.

Alih tidak boleh mengirim isi archive, task content, attachment, raw evidence, atau credential untuk analytics.

Jika telemetry dipertimbangkan di masa depan, desain privacy dan consent harus dibuat secara eksplisit terlebih dahulu.

Untuk Alpha v0.1, feedback dapat dikumpulkan secara manual dari closed tester.

---

34. Closed Alpha

Setelah Alpha v0.1 memenuhi exit criteria, binary diberikan kepada sejumlah kecil tester.

Target awal tidak perlu besar.

Tujuan closed Alpha adalah menemukan masalah nyata pada:

- authentication;
- workspace selection;
- large Workspace behavior;
- unusual hierarchy;
- attachment volume;
- pagination;
- network reliability;
- archive size;
- backup duration;
- filesystem behavior;
- report comprehension;
- user trust;
- perceived usefulness.

Closed Alpha bukan public launch.

---

35. Alpha Validation Questions

Closed Alpha harus membantu menjawab:

Usability

- Apakah user memahami cara melakukan authentication?
- Apakah "alih backup" cukup jelas?
- Apakah user tahu di mana archive tersimpan?
- Apakah user memahami status verification?

Reliability

- Apakah backup berhasil pada Workspace nyata?
- Apakah large Workspace menyebabkan masalah?
- Apakah network interruption menghasilkan failure yang benar?
- Apakah attachment preservation tetap reliable?

Comprehension

- Apakah Recovery Report dapat dipahami?
- Apakah "VERIFIED_WITH_LIMITATIONS" membingungkan?
- Apakah user memahami bahwa PARTIAL/UNKNOWN tidak berarti data source kosong?

Value

- Apakah user merasa portable archive berguna?
- Apakah verification meningkatkan trust?
- Apakah user benar-benar memiliki kekhawatiran tentang SaaS data portability?
- Apakah mereka ingin menjalankan backup lagi?

---

36. Feedback Collection

Feedback closed Alpha dapat dikumpulkan secara manual.

Tidak perlu membangun:

- analytics dashboard;
- feedback backend;
- telemetry pipeline;
- CRM integration.

Minimal informasi yang berguna:

Alih version
OS
Workspace approximate size
Backup duration
Final verification status
Failure stage if any
User feedback

Jangan meminta tester mengirim credential atau raw archive kecuali ada kebutuhan debugging yang jelas dan user memahami isi datanya.

---

37. Out of Scope

Alpha v0.1 tidak mencakup:

- GUI;
- desktop application;
- cloud backend;
- hosted SaaS;
- billing;
- subscription;
- payment processing;
- scheduled backup;
- automatic background backup;
- restore engine;
- migration engine;
- production OAuth;
- connector kedua;
- multi-SaaS orchestration;
- synchronization;
- source modification;
- public marketplace distribution;
- mandatory telemetr
- mobile application;
- browser extension;
- team administration;
- enterprise management console;
- automatic cloud upload;
- full resumability;
- advanced localization/i18n.
Fitur tersebut hanya boleh dipertimbangkan setelah evidence menunjukkan kebutuhan nyata.

38. Explicit Non-Goals
Alpha v0.1 tidak bertujuan:
- menjadi pengganti ClickUp;
- menjadi ClickUp client;
- menjadi migration platform;
- menjadi synchronization service;
- menjadi backup SaaS cloud;
- menjamin recovery seluruh ClickUp semantics;
- menjamin point-in-time source consistency;
- menyembunyikan source limitations;
- mendukung semua SaaS;
- memiliki polished consumer UX.
Tujuan Alpha tetap sempit:
Allow another person to create and independently verify a local portable archive of their ClickUp data without understanding Alih's internal pipeline.

39. Alpha Exit Criteria
Alpha v0.1 dianggap siap untuk external closed testing apabila:
- alih auth bekerja dari distributed binary;
- alih backup tersedia;
- user tidak perlu menjalankan manual M0–M7 pipeline;
- alih backup menggunakan existing core implementation;
- successful backup selalu melewati independent verification;
- FAILED tidak pernah menghasilkan successful backup;
- INCOMPLETE tidak pernah menghasilkan successful backup;
- VERIFIED dapat menghasilkan successful backup;
- VERIFIED_WITH_LIMITATIONS dapat menghasilkan successful backup dengan limitations tetap terlihat;
- Recovery Report berhasil dihasilkan;
- Recovery Report menggunakan English;
- CLI user-facing output menggunakan English;
- archive location ditampilkan dengan jelas;
- credential tidak bocor;
- source tetap read-only;
- interruption tidak menghasilkan false-complete archive;
- binary tidak mengandung developer secrets;
- tester tidak membutuhkan source repository;
- tester tidak membutuhkan Go toolchain;
- core M0–M7 tidak diduplikasi oleh orchestration layer.

40. After Alpha
Setelah closed Alpha menghasilkan evidence yang cukup, keputusan berikutnya dibuat berdasarkan hasil penggunaan nyata.
Potential directions:
- desktop utility;
- improved authentication;
- production OAuth;
- scheduled local backups;
- additional connectors;
- restore/migration tooling;
- packaging/installers;
- cloud features;
- commercial model.
Tidak satu pun dari direction tersebut dianggap sebagai kelanjutan otomatis.
Prioritas ditentukan berdasarkan:
observed user problem
        ↓
Alpha evidence
        ↓
product decision
        ↓
implementation
bukan:
feature idea
        ↓
implementation
        ↓
hope somebody needs it
Productization Principle
Alih tidak perlu menjadi platform besar untuk memberikan value.
Alpha v0.1 mempertahankan tujuan yang sederhana:
Take control of your SaaS data.
Dan untuk fase ini, Alih hanya perlu melakukan satu pekerjaan dengan sangat baik:
Create a local, portable, evidence-backed and independently verified archive of a user's ClickUp data — without modifying the source.
