# tong

**tong** (同, from 同步 *tóngbù* — "to synchronize") is a free and open-source
command-line tool that gives you an **rsync-compatible CLI** for synchronizing
files between local directories and **cloud object storage**, starting with
Amazon S3. More providers are planned — see the
[supported storage](#supported-storage) checklist.

If you know `rsync`, you already know `tong`:

    tong -a --delete ./dist/ s3://example-bucket/dist/

> [!WARNING]
> tong is under active development. The v0.2 CLI surface is implemented, but
> `-z`, `-c` and `-P` are still rejected with a usage error until they
> land.

## Supported storage

Boxes are ticked as support lands. Local filesystems and Amazon S3 (and the
services that speak its API) are the backends of v0.2.

- [x] Local filesystem — `/path`
- [x] Amazon S3 — `s3://bucket/key`
- [x] S3-compatible services — MinIO, Cloudflare R2, Backblaze B2, Wasabi, … (`s3://` + `--s3-endpoint`)
- [ ] Google Cloud Storage — `gs://bucket/key`
- [ ] Azure Blob Storage — `az://container/key`

Credentials follow each provider's standard chain, so tong needs no config
file:

| Provider               | Credentials                                                        |
| ---------------------- | ------------------------------------------------------------------ |
| Local filesystem       | —                                                                  |
| Amazon S3              | Standard AWS chain: env vars, `~/.aws`, IAM roles                  |
| S3-compatible services | Same as S3, plus `--s3-endpoint`                                   |
| Google Cloud Storage   | Application Default Credentials / `GOOGLE_APPLICATION_CREDENTIALS` |
| Azure Blob Storage     | `AZURE_STORAGE_ACCOUNT` + key/SAS token, or Entra ID               |

## Why tong?

- **rsync muscle memory** — familiar positional arguments (`SRC DEST`),
  short flags (`-a`, `-v`, `-n`, `-z`), and rsync-compatible exit codes.
  No new mental model, no YAML config file.
- **Object storage first** — mtime is preserved via object metadata, uploads
  are verified with checksums, and listings are paged efficiently so large
  buckets don't blow up memory.
- **Everywhere storage** — one binary, one command, any combination of local
  paths and remote buckets on either side (local → cloud, cloud → local,
  cloud → cloud).
- **Safe by default** — `--dry-run` prints exactly what would happen before
  you commit to it; `--delete` never runs implicitly.

## Features

- rsync-compatible interface: option names, combined short flags
  (`-avz`), and exit codes.
- Incremental transfers: skip unchanged files by size, and by mtime with
  `-t`, `--update` or a move (see [rsync compatibility](#rsync-compatibility)).
- Mirror remote trees with `--delete`, including `--dry-run` previews.
- Include/exclude pattern filters (`--include`, `--exclude`,
  `--exclude-from`).
- Size and time constraints (`--max-size`, `--min-size`, `--update`,
  `--existing`, `--ignore-existing`).
- Parallel transfers (`--parallel`, four files at a time by default) and
  multipart uploads, so objects beyond the 5 GB single-request limit go up
  in parts.
- One shared bandwidth budget for the whole run (`--bwlimit`), covering every
  worker and both directions.
- One-way moves with `--remove-source-files`.
- Machine-readable summaries with `--stats`.
- Still planned for later releases: progress reporting with `-P`, resumable
  uploads, compression with `-z`, server-side copy when syncing bucket →
  bucket, and checksum comparison with `-c`.

## Installation

### From source

Requires [Go](https://go.dev) 1.27 or newer:

    git clone https://github.com/lucidfrontier45/tong
    cd tong
    go build ./cmd/tong

### Prebuilt binaries

Download a static binary for your platform from the
[Releases](https://github.com/lucidfrontier45/tong/releases) page and put
it on your `PATH`.

## Quick start

Sync a local directory to S3 (mirror, remove extraneous files):

    tong -a --delete ./site/ s3://example-bucket/site/

Preview what would happen without changing anything:

    tong -avn --delete ./site/ s3://example-bucket/site/

Pull a bucket back down to your laptop:

    tong -a s3://example-bucket/backups/ ./backups/

Copy between buckets (server-side copy where the provider supports it):

    tong -a s3://source-bucket/data/ s3://dest-bucket/data/

Sync against an S3-compatible endpoint (MinIO, R2, B2, Wasabi, …):

    tong -a --s3-endpoint https://<accountid>.r2.cloudflarestorage.com \
        ./logs/ s3://my-bucket/logs/

Filter what gets transferred:

    tong -a --include '*/' --include '*.png' --include '*.jpg' --exclude '*' \
        ./images/ s3://example-bucket/images/

## CLI

    tong [OPTION]... SRC... [DEST]

Exactly like rsync, the last argument is the destination and every preceding
argument is a source. Sources and destinations are URLs:

| Scheme               | Meaning                                 | Status  |
| -------------------- | --------------------------------------- | ------- |
| `/path`              | Local filesystem (relative or absolute) | v0.1    |
| `s3://bucket/key`    | AWS S3 and S3-compatible services       | v0.1    |
| `gs://bucket/key`    | Google Cloud Storage                    | planned |
| `az://container/key` | Azure Blob Storage                      | planned |

### Options (rsync-compatible subset)

| Option              | Description                                            |
| ------------------- | ------------------------------------------------------ |
| `-r, --recursive`   | Recurse into directories                               |
| `-a, --archive`     | Archive mode: `-r -t` (times are preserved; ownership and permissions do not apply to object storage) |
| `-v, --verbose`     | Increase verbosity (repeatable)                        |
| `-n, --dry-run`     | Show what would be transferred without changing anything |
| `--delete`          | Delete extraneous files from the destination           |
| `-t, --times`       | Preserve modification times                            |
| `-z, --compress`    | Compress file data during transfer                     |
| `-c, --checksum`    | Skip based on checksums, not size + mtime              |
| `--size-only`       | Skip based on size only                                |
| `-u, --update`      | Skip files that are newer on the destination           |
| `--existing`        | Skip creating new files on the receiver                |
| `--ignore-existing` | Skip files that already exist on the receiver          |
| `--include`/`--exclude PATTERN` | Filter the transfer list                    |
| `--exclude-from FILE` | Read exclude patterns from a file                    |
| `--max-size SIZE`   | Don't transfer files larger than SIZE                  |
| `--min-size SIZE`   | Don't transfer files smaller than SIZE                 |
| `--parallel N`      | Transfer up to N files at a time (default 4; a tong extension, rsync transfers a single stream) |
| `--bwlimit RATE`    | Limit I/O bandwidth (rsync's rate grammar, see below)  |
| `-P`                | Same as `--partial --progress`                         |
| `--stats`           | Print a transfer summary as `key=value` lines          |
| `--remove-source-files` | Delete source files that are in sync with the destination (one-way move) |
| `--s3-endpoint URL` | Use a custom S3-compatible endpoint                    |
| `--s3-trust-conditional-delete` | Allow removing sources on a custom endpoint verified to enforce `If-Match` |
| `--s3-region REGION`| Override the AWS region                                |
| `-h, --help`        | Show help                                              |

Deliberately not applicable to object storage: `-e/--rsh` (no shell transport),
`--perms`/`--owner`/`--group`/`--devices`/`--links` (no POSIX metadata),
daemon mode. tong exits with an error if it is given options it cannot honor.

### rsync details worth knowing

- **A trailing separator on the source decides what is copied.**
  `tong -a ./dist/ s3://bucket/dist/` copies the *contents* of `./dist` into
  `s3://bucket/dist/`, while `tong -a ./dist s3://bucket/dist/` copies the
  *directory* `dist` into the destination, so the files end up in
  `s3://bucket/dist/dist/`.
- **A single source file is copied onto the destination itself.**
  `tong a.txt b.txt` writes `b.txt`, and `tong a.txt s3://bucket/b.txt`
  uploads the object `s3://bucket/b.txt`.
- **The filters follow rsync, including its sharp edges.** The first rule
  that matches a path wins, and an excluded directory takes its contents with
  it. To copy only some file types from a whole tree, include the directories
  as well:

      tong -a --include '*/' --include '*.png' --exclude '*' ./images/ s3://example-bucket/images/

  The patterns match the path of the file in the destination, which is the
  transfer root of rsync: a source without a trailing separator keeps its own
  name in that path, so `--exclude dist/sub` prunes `dist/sub` when
  `dist` is the source.
- **`--delete` needs `-r` or `-a`.** A sync that does not recurse does
  not know what the destination should hold, so rsync refuses the
  combination and so does tong.
- **A source has to exist.** tong reports a missing source and leaves the
  destination alone, instead of treating it as an empty directory that
  `--delete` would empty.
- **Modification times are preserved through object metadata.** tong stores
  the modification time of a file in the `x-amz-meta-mtime` metadata of the
  object, with the one second resolution that object storage offers.
- **Multipart uploads are planned from the listing.** A file larger than
  16 MiB goes up in parts of at least 16 MiB, four at a time, and the size
  tong knows from the listing lets the part size grow, so a single object may
  be as large as S3 allows. The number of parts in flight is fixed rather than
  taken from `--parallel`; see
  [rsync compatibility](#rsync-compatibility). An upload that fails aborts its
  parts instead of leaving them in the bucket.
- **`--bwlimit` takes rsync's rate grammar.** The value is a rate per second:
  a bare number is KiB/s, so `--bwlimit 100` means 100 KiB/s. The suffixes are
  rsync's size suffixes: `B`, `K`, `M`, `G`, `T` or `P`, where a single letter
  or a `…iB` form is a multiple of 1024 and a two-letter form ending in `B` is
  a multiple of 1000 — so `1K` and `1KiB` are 1024 B/s while `1KB` is
  1000 B/s. Fractions work (`1.5m`), letters are case-insensitive, and `0`
  means no limit. A positive value that works out to zero bytes per second is
  a usage error rather than a limit that quietly disappears. Unlike rsync, the
  budget is shared by every worker and by the parts of a multipart upload, so
  `--parallel 8 --bwlimit 1M` still moves about 1 MiB/s in total. Throttling
  happens on the source, so it holds for local → S3, S3 → local and S3 → S3
  alike.
- **`--remove-source-files` removes what is in sync, not only what was
  copied.** A source file that the destination already holds is removed as
  well, because it is in sync either way. Files that `--ignore-existing`,
  `--update` or a filter leaves out stay where they are, directories are
  never removed, and nothing is removed during a `--dry-run`, after a failed
  transfer, or once the run has been interrupted. Because removal loses data,
  a move is stricter than a copy: the modification times are compared even
  without `-t`, a source is only removed if it still matches what was listed,
  and a source that resolves to the destination itself is left alone. See
  [rsync compatibility](#rsync-compatibility).
- **`--stats` prints `key=value` lines, not rsync's summary block.** The
  keys are stable, so a script can read them with `grep` or `awk`:

      files_total=42
      files_transferred=40
      files_skipped=2
      files_deleted=1
      files_removed=40
      bytes_transferred=1234
      errors=0

  `files_total` counts the files in the transfer list, `files_skipped` the
  ones that were already in sync, `files_deleted` the extraneous destination
  files that `--delete` removed and `files_removed` the source files that
  `--remove-source-files` removed. The summary is printed for a dry run and
  for a run that failed partway through as well.

- **`-u` compares the preserved modification times.** The time an object
  was uploaded is not the modification time of the file, so `--update` looks
  at the preserved times, the same way `--times` does. Otherwise a file that
  was uploaded long ago would look older than the copy on the destination
  that was written from a newer source.

### Exit codes

Exit codes follow rsync conventions so scripts written for rsync keep working:

| Code | Meaning                                      |
| ---- | -------------------------------------------- |
| 0    | Success                                      |
| 1    | Syntax or usage error, including an ambiguous move |
| 2    | Protocol incompatibility                     |
| 3    | Errors selecting input/output files          |
| 20   | Received `SIGINT`/`SIGTERM` mid-run (see [rsync compatibility](#rsync-compatibility)) |
| 23   | Partial transfer due to error                |
| 24   | Source files vanished before transfer        |

## rsync compatibility

tong aims at rsync's CLI, not at byte-for-byte conformance. The table below
records every place where behaviour was decided explicitly against rsync
3.5.0, because the two tools can disagree there. Rows marked *same* are
verified identical; the rest are deliberate divergences. Behaviour is checked
against the binary rather than against the man page.

| Area | rsync | tong |
| --- | --- | --- |
| `--parallel` | no such option (single stream) | transfers up to N files at once |
| `--bwlimit` scope | one budget per stream | one budget for the whole run, workers and multipart parts included |
| `--bwlimit` bare number | KiB/s | same |
| `--bwlimit` suffixes | `B/K/M/G/T/P`, `…iB` = 1024, `…B` = 1000, fractions | same |
| `--bwlimit` rounding | rounds to whole KiB, minimum 512 B/s | keeps the exact rate, no floor above 0, rejects a positive rate that resolves to zero |
| `--stats` output | multi-line summary block | stable `key=value` lines |
| Default quick check | size **and** mtime | size only (no extra request per object) |
| Quick check in move mode | size **and** mtime | size and mtime |
| `--remove-source-files` and mtime | compares mtimes, does not preserve them without `-t` | same |
| Two sources, one destination key | first source wins; the other source is left alone | copy: same. move: refused with exit 1 before anything is written or deleted |
| Source equal to destination | refuses to remove, exit 23 | same |
| Source replaced after listing | refuses to remove the changed file, exit 23 | same |
| Multipart part concurrency | n/a | fixed, independent of `--parallel` |
| `SIGINT`/`SIGTERM` | stops, exits 20, keeps sources | same |

**Why the default quick check is size-only.** rsync compares size and mtime,
but on object storage an mtime comparison costs one `HeadObject` per object,
which is what `--times` and `--update` already pay for and what a plain copy
avoids. tong therefore treats a size match as "in sync" unless an option asks
for the time. This is a real divergence: with equal sizes and different
mtimes, rsync rewrites the destination and tong leaves it alone. Pass `-t` or
`--update` to get rsync's behaviour. A move always compares the times, because
a size match alone is not enough evidence to delete the source.

**Why a move is stricter than a copy.** Removing a source is the only
operation here that can lose data, so a move adds two guards: it compares
mtimes even without `-t`, and it deletes a source only after confirming that
the file still matches the listing it planned from. A source that resolves to
the destination itself — including through a symlink, or under another
spelling of the same name on a case-insensitive filesystem — is left in place
with exit 23, and two sources that would write one destination key are
refused with exit 1 before anything is written or deleted.

**How a move checks a source before removing it.** A local file is compared by
size, modification time, and filesystem identity, so replacing it with a
same-size, same-mtime file is detected. Tong moves the entry to a private
directory on the same filesystem, then checks it before unlinking it. If it
changed, tong restores it without overwriting a newer arrival. If restoration
is not possible, tong exits 23 and prints the staged file's recovery path.
For moves to local storage, tong also checks the completed destinations
before deleting any source: if the filesystem resolves two planned names
to one file, copies may have been written, but both sources are retained. A local
move still expects nothing to rewrite the source while it is being read; a
source that changes during a transfer is detected before it is removed. S3
removal uses an atomic conditional delete on the listed ETag. Source removal
is disabled by default on custom S3 endpoints, including overrides from the
AWS SDK configuration. Pass
`--s3-trust-conditional-delete` only after verifying the endpoint enforces
`If-Match` on `DeleteObject`. An endpoint that ignores the header, such as
LocalStack 3.8.1, can delete a replacement object. Copies to or from these
endpoints do not require the flag. Before removing an already-in-sync source,
tong also rechecks the destination. If the destination disappeared or either
file changed since listing, tong keeps the source and exits 23.

Before writing or deleting anything, moves reject multiple writes to the
same destination entry, conflicting local file/directory destinations, and
overlapping source/destination mappings. Destination pruning keeps the
entries targeted by a transfer, even through directory symlinks.

**Why part concurrency is not `--parallel`.** The SDK allocates
`Concurrency+1` part buffers per object, so tying part concurrency to the
file-level knob would multiply memory twice. `--parallel` is a file-level
setting only, and the part concurrency is a fixed value in the driver. Peak
buffering stays around `--parallel × (part concurrency + 1) × part size`,
which is why the part size is not larger than it has to be.

## How tong compares

|                        | rsync            | rclone                | tong       |
| ---------------------- | ---------------- | --------------------- | ---------- |
| rsync CLI compatible   | yes              | no                    | **yes**    |
| Cloud object storage   | no               | yes                   | **yes** — local, S3 and S3-compatible services |
| POSIX metadata (perms, ownership, symlinks) | yes | partial | no (object storage semantics) |
| Config file required   | no               | yes                   | **no**     |
| Delta (rolling-checksum) transfer | yes   | no                    | planned    |

rclone is an excellent and far more featureful tool; tong exists for people
who want to keep typing rsync flags — in CI pipelines, cron jobs, scripts, and
shell history — and have them just work against a bucket.

## Roadmap

- [x] v0.1 — local ⇄ S3: core flags, `--dry-run`, `--delete`, filters, exit codes
- [x] v0.2 — parallel transfers (`--parallel`), multipart uploads beyond 5 GB, `--bwlimit`, `--remove-source-files`, `--stats`
- [ ] v0.3 — Google Cloud Storage and Azure Blob Storage
- [ ] v0.4 — cloud ⇄ cloud server-side copy
- [ ] v0.5 — checksum-based comparison (`-c`) using provider checksum APIs
- [ ] v1.0 — stable CLI contract, delta transfer for large files

## Development

    go mod download       # fetch dependencies
    go build ./...        # build
    go test ./...         # run tests
    go test -race ./...   # run tests with the race detector

Linting and formatting use golangci-lint v2:

    golangci-lint run --fix
    golangci-lint fmt

Install the pre-commit hook to format automatically before each commit:

    pre-commit install

The tests of the S3 driver need a running S3 service and are skipped without
one. Point them at any S3-compatible endpoint:

    export TONG_TEST_S3_ENDPOINT=http://127.0.0.1:9000
    export TONG_TEST_S3_BUCKET=tong-test
    export AWS_ACCESS_KEY_ID=minio
    export AWS_SECRET_ACCESS_KEY=minio123
    go test ./...

## Contributing

Bug reports, feature requests, and pull requests are welcome. Please open an
issue before starting large changes so the CLI contract can be discussed
first. Run `go test ./...` and `golangci-lint run` locally before submitting.

## License

[MIT](LICENSE) © 杜世橋 Du Shiqiao
