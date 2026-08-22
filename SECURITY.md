# Security Policy

Peeper is pre-release software. No stable release or maintained version branch
exists yet. Security fixes target `main`; older commits, forks, and downloaded
snapshots receive no guaranteed updates.

## Report privately

Do not open a public issue for a suspected vulnerability.

Email `fuad.cs22@gmail.com` with subject `[Peeper Security]` and include:

- affected compiler revision or `peeper -version` output;
- host and target operating system and architecture;
- vulnerability description and expected impact;
- minimal source, manifest, package, or protocol input needed to reproduce it;
- reproduction steps and relevant diagnostics or generated output;
- whether details are already public.

Reports are handled on a best-effort basis while the project is pre-release.
Maintainers will acknowledge a received report, investigate it, and coordinate
disclosure when the issue is within Peeper's control. Please avoid publishing
details until a fix or disclosure plan is agreed.

## Scope

Security-sensitive areas include:

- compiler crashes or miscompilation with safety consequences;
- ownership, borrow, lifetime, allocator, or bounds-check bypasses;
- package download, cache, manifest, or lockfile vulnerabilities;
- LSP inputs that permit unintended file or process access;
- bundled-library or generated-code defects with security impact.

General bugs without security impact belong in the
[public issue tracker](https://github.com/PeeperLanguage/compiler/issues).
Vulnerabilities in Go, LLVM, Clang, Git, or another dependency should normally
be reported upstream unless caused by Peeper's integration.
