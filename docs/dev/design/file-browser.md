# Files on the phone

Status: design, 2026-11-04.

Away from the desk, an agent says it wrote a report or an image. The app
should show it. This adds a read-only file browser beside the existing
Changes view ([ws-contract.md](../ws-contract.md) "Changes"). Nothing here
writes a file.

The screens are on the
[design canvas](https://claude.ai/code/artifact/01ec448b-076c-43be-aa21-ab4205c69bc6).

## 1. Decisions

| # | Decision | Why |
|---|---|---|
| D1 | The root is the agent's folder or the space's folder, both known to the daemon. A client only ever names a path relative to it. | Like Changes: a client can't ask for a folder. |
| D2 | Every open goes through `os.Root`. | It refuses `..` and symlinks that leave the root, including a swap between a check and the open. A prefix check does not. |
| D3 | Sensitive names are hidden in code, in `internal/files`: not listed, not searchable, not readable. | A phone can be lost, and a safety rail stays in code. |
| D4 | Only regular files are read. The type comes from the file handle's stat, after the open, which is non-blocking. | A FIFO would hang the read. |
| D5 | Text comes back as JSON, images as bytes with `nosniff` and a fixed content type. SVG is text. | The app never renders active content. |
| D6 | New feature name `files` in `bot-ready`. `/ws` protocol stays 1. | Optional part of the contract. An older daemon gives no entry point. |

## 2. Routes

All `accessDevice`, `GET` only.

| Route | Body |
|---|---|
| `/api/agents/{pane_id}/files?path=<rel>&q=<text>` | list |
| `/api/agents/{pane_id}/files/{path...}` | one file |
| `/api/spaces/{id}/files?path=<rel>&q=<text>` | list |
| `/api/spaces/{id}/files/{path...}` | one file |

List:

```json
{"root_name": "shop", "path": "docs", "truncated": false, "hidden": 2,
 "entries": [{"name": "report.md", "path": "docs/report.md", "kind": "file",
              "size": 1204, "modified": "2026-11-03T21:02:11Z"}]}
```

- `kind` is `file`, `dir` or `link`. A link that leaves the root is listed
  with `"blocked": true`.
- Folders first, then by name; the app can sort by `modified`.
- `hidden` counts what the sensitive rule left out, so the app can say so.
- `q` searches names under `path` (not contents), case-insensitive, skipping
  `.git`, `node_modules` and the hidden names.

One file: JSON unless `?raw=1`.

```json
{"path": "docs/report.md", "size": 1204, "modified": "…", "kind": "text",
 "text": "…", "truncated": false}
```

`kind` is `text`, `image` or `binary`. `image` and `binary` carry no `text`.
`?raw=1` serves an image's bytes; on a file that is not an image it is
`415`.

## 3. Limits

| Limit | Value |
|---|---|
| Entries in a list | 2,000, then `truncated` |
| Names read from one folder | 20,000, then `truncated` |
| Text returned | 1 MiB, then `truncated` |
| Image served | 16 MiB, then `413` |
| Search | 20,000 entries seen, 200 matches, 2 s, then `truncated` |

## 4. Errors

| Status | `reason` | Meaning |
|---|---|---|
| 404 | `no_agent`, `no_space` | No such agent or space. |
| 404 | `no_cwd` | The agent or space has no folder yet. |
| 404 | `not_found` | Gone, or the folder was removed. |
| 403 | `outside` | Left the root. |
| 403 | `sensitive` | Hidden for safety. |
| 403 | `not_file` | A read of a folder, FIFO, socket or device. |
| 403 | `denied` | The system refused, or a link loops. |
| 413 | `too_large` | Over the image limit. |
| 415 | `not_image` | `?raw=1` on a file that is not an image. |
| 400 | `bad_path` | Absolute, or not a clean relative path. |
| 500 | `failed` | Anything else. The message is fixed words, never a path. |

`not_file` and `not_image` were added in PR 1; the contract is
[ws-contract.md](../ws-contract.md) "Files".

## 5. Sensitive names

Matched on any path segment, under Unicode case folding with NFC (a
case-folding filesystem such as APFS opens the real file for `id_rſa`, with
a long s, or a Kelvin sign for `k`): `.git`, `.env` and `.env.*` (except
`.env.example`, `.env.sample`, `.env.template`), `.envrc`, `.git-credentials`,
`.pgpass`, `.ssh`, `.aws`, `.gnupg`, `.netrc`, `.npmrc`, `.pypirc`, `.docker`,
`.kube`, `id_rsa*`, `id_ed25519*`, `id_ecdsa*`, `id_dsa*`, and names ending
`.pem`, `.key`, `.p12`, `.pfx`, `.keystore`, `.p8`, `.tfvars`, plus
`credentials`, `credentials.json`, `secrets.json`, `secrets.yaml`,
`secrets.yml`, `.codex`, `.claude`, `Library`, `.credentials.json`, `auth.json`, `.gcloud`, and shell and REPL history names. `node_modules` is skipped by a search under the same fold. Repository `.config`, `.local` and `.cache` folders remain visible.

The home folder and the daemon's data folder, their parents, and folders inside the data folder are refused as `sensitive`, including when reached through a symlink. Choose a project folder instead.

A root with a sensitive segment anywhere on its path (an agent working in
`.git` or `~/.ssh`, spelled or reached through a link) is refused as
`sensitive`. Changes ([ws-contract.md](../ws-contract.md) "Changes") uses the
same rule, on the agent's folder and on the repository root, both cleaned and
symlink-resolved. The fold also drops default-ignorable code points (zero
width, soft hyphen, variation selectors), so `.​env` is `.env`.

Two known limits. Changes follows the repository root, which can be above
the agent's folder. A sensitive file renamed to an ordinary name is no
longer hidden.

This is a rail against the likely case, not a promise: the docs say a paired
phone can read the agents' folders.

## 6. Audit

Each read logs `files.read` with `pane_id` or space, `path`, and size, never
contents; a refused read logs its `reason` instead of the size. A list logs
nothing.

## 7. The app

- `SheepdogKit`: models, `FilesClient`, `FileKind` and sort logic, tested with
  fixtures in `testdata/protocol/rest/files/` (a folder of its own, so the
  app's golden test, which lists `rest/`, takes them up with the types).
- Views: the Files list with a breadcrumb and search, Markdown with a raw
  toggle, images with zoom, code with line numbers. iPad: list and viewer side
  by side.
- Entry points: an agent's ⋯ menu beside Changes, and the space detail.
- Offline, gone, denied, hidden and too large each have a plain message.
- Docs: [iphone-app.md](../../iphone-app.md), [privacy.md](../../privacy.md),
  [ws-contract.md](../ws-contract.md).

## 8. Work breakdown

1. `feat(api): read-only files`: `internal/files` (root, rules, list, read,
   search; pure, table-tested, with an escape test per rail), routes, feature
   flag, goldens, contract docs.
2. `feat(ios): files browser and viewers`, with UI tests on the demo.
3. Demo data, iPad and the docs.

QA: on a private daemon over Tailscale, on the iPhone 13: a symlink out of the
folder, `.env`, a FIFO, a 2 GB file, a 50,000-entry folder, a folder removed
while open.
