# tong

**tong** (同, from 同步 *tóngbù* — "to synchronize") is a free and open-source
command-line tool that gives you an **rsync-compatible CLI** for synchronizing
files between local directories and **cloud object storage**, starting with
Amazon S3. More providers are planned — see the
[supported storage](#supported-storage) checklist.

If you know `rsync`, you already know `tong`:

    tong -a --delete ./dist/ s3://example-bucket/dist/

> [!WARNING]
> tong is under active development. The v0.1 CLI surface is implemented, but
> the flags that are marked as planned below are rejected with a usage error
> until they land.

## Supported storage

tong is in early development, so nothing is checked off yet. Boxes are ticked
as support lands; v0.1 targets local ⇄ Amazon S3.

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
- Incremental transfers: skip unchanged files by size + mtime.
- Mirror remote trees with `--delete`, including `--dry-run` previews.
- Include/exclude pattern filters (`--include`, `--exclude`,
  `--exclude-from`).
- Size and time constraints (`--max-size`, `--min-size`, `--update`,
  `--existing`, `--ignore-existing`).
- Planned for v0.2 and later: parallel, resumable multipart uploads with
  bandwidth limiting (`--bwlimit`), server-side copy when syncing bucket →
  bucket, progress reporting with `-P`, machine-readable `--stats` output
  and checksum comparison with `-c`.

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
| `--bwlimit KBPS`    | Limit I/O bandwidth, KiB/s                             |
| `-P`                | Same as `--partial --progress`                         |
| `--stats`           | Print a transfer summary                               |
| `--remove-source-files` | Delete source files after a successful transfer (one-way move) |
| `--s3-endpoint URL` | Use a custom S3-compatible endpoint                    |
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
| 1    | Syntax or usage error                        |
| 2    | Protocol incompatibility                     |
| 3    | Errors selecting input/output files          |
| 23   | Partial transfer due to error                |
| 24   | Source files vanished before transfer        |

## How tong compares

|                        | rsync            | rclone                | tong       |
| ---------------------- | ---------------- | --------------------- | ---------- |
| rsync CLI compatible   | yes              | no                    | **yes**    |
| Cloud object storage   | no               | yes                   | planned — S3 first |
| POSIX metadata (perms, ownership, symlinks) | yes | partial | no (object storage semantics) |
| Config file required   | no               | yes                   | **no**     |
| Delta (rolling-checksum) transfer | yes   | no                    | planned    |

rclone is an excellent and far more featureful tool; tong exists for people
who want to keep typing rsync flags — in CI pipelines, cron jobs, scripts, and
shell history — and have them just work against a bucket.

## Roadmap

- [x] v0.1 — local ⇄ S3: core flags, `--dry-run`, `--delete`, filters, exit codes
- [ ] v0.2 — `--stats`, `--bwlimit`, parallel uploads, `--remove-source-files`
- [ ] v0.3 — Google Cloud Storage and Azure Blob Storage
- [ ] v0.4 — cloud ⇄ cloud server-side copy, S3-compatible endpoints
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
