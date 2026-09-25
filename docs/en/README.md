# IMAP Header Sorter

English | [Deutsch](../de/README.md)

Standalone Go service: one container per mailbox. It fetches only `BODY.PEEK[HEADER]` from unseen messages, so it neither downloads the body nor marks mail as read (unless a rule sets `mark_as_read`). TLS certificate verification is enabled. No mail data is sent to an external AI service.

## Start

Run from the project root:

```sh
cp .env.example .env
# Set IMAP_DSN to your mailbox credentials, set CLEAN_FOLDER and check the folder names.
docker compose up -d --build
```

Leave `DRY_RUN=true` initially and inspect `docker compose logs -f`. Set `DRY_RUN=false` only after checking the decisions. Keep `.env` private; it contains a password.

In dry run nothing is moved and all mail stays unread. Each message is logged once and then left out of the following runs, so newer mail gets its turn. All messages are evaluated and logged again after an hour, when `mapping.json`, `whitelist.json` or `blacklist.json` changes, when a destination folder is created or removed, or when the container restarts. Edits to the rules therefore show up in the log at the next run.

## Docker

The service is built and run entirely with Docker; Go does not need to be installed on the host.

### How the image is built

The `Dockerfile` has two stages:

1. **Build stage** (`golang:1.23-alpine`): copies `go.mod`, the `*.go` files and the example config files, runs `go vet` and all tests, and then compiles a single static binary (`CGO_ENABLED=0`, `-trimpath`, stripped with `-ldflags='-s -w'`). A failing test stops the build, so a broken version never becomes an image.
2. **Runtime stage** (`scratch`, an empty image): contains only the compiled binary `/sorter` and the CA certificates needed to verify the server's TLS certificate. There is no shell, no package manager and no Go toolchain in the final image.

The container runs as the unprivileged user `65532:65532`. It opens no ports; it only needs an outgoing connection to the IMAP server (port 993). Its only state is kept in memory, so no volume is needed besides the config directory.

### What Compose does

`compose.yaml` defines one service, `sorter`:

- `build: .`: builds the image from the `Dockerfile` in the project root.
- `env_file`: passes the settings from `.env` (or `ENV_FILE`) into the container as environment variables.
- `volumes`: mounts the config directory (`./config` or `CONFIG_DIR`) read-only at `/config`, where the program reads `mapping.json`, `whitelist.json` and `blacklist.json`.
- `restart: unless-stopped`: restarts the container after a crash or a reboot of the Docker host, unless you stopped it yourself.

The program runs in an endless loop: one run every `POLL_SECONDS`, until the container is stopped. It writes all output to stdout, so `docker compose logs` shows it. The image has no time zone data, so log times are in UTC.

### Common commands

Run all commands in the project root.

| Task | Command |
|---|---|
| Build and start (first time, or after a code change or `git pull`) | `docker compose up -d --build` |
| Follow the log | `docker compose logs -f` |
| Apply a changed `.env` | `docker compose up -d` (recreates the container, no rebuild needed) |
| Apply changed files in `config/` | nothing, they are re-read at the next run |
| Show status | `docker compose ps` |
| Restart | `docker compose restart` |
| Stop and remove the container | `docker compose down` |
| Build and run the tests only | `docker compose build` |

Files in the config directory must be readable for the container user (UID 65532). The default permissions of new files (`644`) are fine; a file restricted to its owner (`600`) cannot be read and aborts the run.

### Several mailboxes

Each mailbox needs its own container with its own settings and config directory. The simplest way is a separate copy of the project directory per mailbox. To run several mailboxes from one directory instead, give each one a Compose project name and its own env file:

```sh
# mailbox2.env contains all settings for the second mailbox, plus:
#   ENV_FILE=mailbox2.env
#   CONFIG_DIR=./config-mailbox2
docker compose -p mailbox2 --env-file mailbox2.env up -d --build
docker compose -p mailbox2 --env-file mailbox2.env logs -f
```

`--env-file` makes Compose read `ENV_FILE` and `CONFIG_DIR` from that file, so the container gets the second mailbox's settings and config directory. Pass `-p` and `--env-file` with every command for that mailbox.

### Running without Docker (development)

With Go 1.23 or newer installed, the tests and the program also run directly:

```sh
go vet ./... && go test ./...

set -a; . ./.env; set +a
MAPPING_FILE=config/mapping.json WHITELIST_FILE=config/whitelist.json BLACKLIST_FILE=config/blacklist.json go run .
```

The file paths are needed because the defaults point to `/config`, which only exists inside the container.

## Settings

All settings are environment variables from `.env`. Changes require `docker compose up -d`.

| Variable | Default | Meaning |
|---|---|---|
| `IMAP_DSN` | – (required) | `imaps://user:password@host:993`. URL-encode special characters in user and password (`@` → `%40`, `:` → `%3A`, `/` → `%2F`). Port defaults to 993. |
| `DRY_RUN` | `true` | Log decisions without moving mail. |
| `SOURCE_FOLDER` | `INBOX` | Folder whose unread mail is sorted. |
| `TARGET_FOLDER` | `Junk` | Spam folder. |
| `CLEAN_FOLDER` | – (required) | Folder for clean mail that no rule claims. |
| `UNSURE_FOLDER` | – | Folder for mail the sorter is not sure about. Required when `unsure_threshold` or `SPAM_UNSURE_SCORE` is used. |
| `SPAM_SCORE` | `5` | Spam score at or above which mail is spam. Must be at least 1. |
| `SPAM_UNSURE_SCORE` | off | Spam scores from this value up to `SPAM_SCORE` go to `UNSURE_FOLDER`. Must be between 1 and `SPAM_SCORE`-1. |
| `POLL_SECONDS` | `300` | Seconds between runs, minimum 30. An invalid or smaller value is replaced by 300, with a warning at startup. |
| `MAX_PER_RUN` | `100` | Maximum number of messages per run, oldest first. Messages that stayed in the source folder in an earlier run are left out (see Start). `0` processes nothing. |
| `REQUIRE_DMARC_FOR_LISTS` | `true` | Apply the whitelist and `from` rule conditions only to verified senders (`dmarc=pass`, or a matching `dkim=pass` if the server reports no DMARC result; see Sender verification). |
| `AUTHSERV_ID` | – | Name your mail server uses in its `Authentication-Results` headers, e.g. `mail.example.org`. Recommended, see Sender verification. |
| `MAPPING_FILE` | `/config/mapping.json` | Path of the rules file inside the container. |
| `WHITELIST_FILE` | `/config/whitelist.json` | Path of the whitelist inside the container. |
| `BLACKLIST_FILE` | `/config/blacklist.json` | Path of the blacklist inside the container. |
| `CONFIG_DIR` | `./config` | Host directory that `compose.yaml` mounts at `/config`. Read by Docker Compose, not by the program (see Several mailboxes). |
| `ENV_FILE` | `.env` | Env file that `compose.yaml` passes to the container. Read by Docker Compose, not by the program; only needed for several mailboxes in one directory. |

Switches accept `true`/`false`, `t`/`f` and `1`/`0`, in any case (e.g. `TRUE` or `False`). Any other value, such as a switch set to `no` or a number like `5,5`, is reported as an error and aborts the run instead of being replaced silently. The only exception is `POLL_SECONDS`, which falls back to 300 (see above), so a typo cannot make the container restart in a loop.

## Classification

The spam score is deliberately conservative. DMARC fail scores 4, SPF fail 2, DKIM fail 1, a different Reply-To domain 1, and an upstream `X-Spam-Flag` or `X-Spam-Status` containing "yes" 5 each. DMARC pass subtracts 2. At the default threshold of 5, a message needs several signals to count as spam, e.g. DMARC fail plus DKIM fail, or an upstream spam flag without DMARC pass. With DMARC pass, an upstream spam flag alone scores only 3 and is not spam.

The spam score ignores the subject (only the mapping rules use it). SPF, DKIM and DMARC results are taken from the `Authentication-Results` headers already present in the mail; set `AUTHSERV_ID` so that only your own server's results count (see Sender verification). No model training or LLM is included yet.

## Sorting

Every unseen message in `SOURCE_FOLDER` is first judged, using the first check that matches:

1. Sender on `config/whitelist.json`: clean, no spam check.
2. Sender on `config/blacklist.json`: spam.
3. Otherwise the header score decides: at or above `SPAM_SCORE` spam; at or above the optional `SPAM_UNSURE_SCORE` unsure; below clean.

Spam is moved to `TARGET_FOLDER`, unsure mail to `UNSURE_FOLDER`. Clean mail is routed by the rules in `config/mapping.json` (see Mapping rules), otherwise it goes to `CLEAN_FOLDER`. The rules therefore never rescue spam.

Both lists are JSON arrays of e-mail addresses, compared case-insensitively against the address in the `From` header:

```json
["alice@example.com", "newsletter@example.org"]
```

### Mapping rules

`config/mapping.json` contains rules. Each rule has a destination folder and a list of conditions. A condition compares a field with a pattern and adds its score (which may be negative) when it matches. The best-scoring rule decides:

- score at or above `threshold` (default 5): moved to the rule's folder
- score at or above `unsure_threshold` (optional, between 1 and `threshold`-1): moved to `UNSURE_FOLDER`
- otherwise: moved to `CLEAN_FOLDER`

On a tie, the rule listed first wins.

```json
{
  "threshold": 5,
  "unsure_threshold": 3,
  "rules": [
    {
      "name": "Invoices",
      "folder": "Finance/Invoices",
      "conditions": [
        { "field": "from", "pattern": "%@shop.example", "score": 3 },
        { "field": "subject", "pattern": "*invoice*", "score": 3 },
        { "field": "subject", "pattern": "*reminder*", "score": -2 }
      ]
    }
  ]
}
```

With a verified sender, an invoice from `shop.example` scores 6 and is moved to `Finance/Invoices`. Any other mail from `shop.example`, or an invoice from another sender, scores 3 and goes to `UNSURE_FOLDER`. An invoice reminder from `shop.example` scores 4 and also goes to `UNSURE_FOLDER`. Without verification, the `from` condition scores 0 (see Sender verification), so the invoice from `shop.example` scores only 3 and goes to `UNSURE_FOLDER`.

The folder names in this README and in `mapping.example.json` use `/` between levels. On a server that uses `.` instead, write `Finance.Invoices` (see Folders).

- `field`: `from` is the sender address; `subject` or any other header name (e.g. `to`, `list-unsubscribe`) is compared with the full header value. Encoded headers such as UTF-8 subjects are decoded first. A header that appears several times is joined into one value, separated by spaces. A condition on a missing header never matches.
- `pattern`: `*` and `%` match any characters, case is ignored, and the pattern must match the whole value. `%@example.com` covers a whole domain (but not subdomains), `*invoice*` matches "invoice" anywhere in the subject, and a pattern without wildcards must match exactly.
- `name` is optional and shows up in the log, e.g. `RULE:"Invoices"=6` or `RULE_UNSURE:"Invoices"=3`. Rules without a name are logged as `rule 1`, `rule 2` and so on, by position.
- `trust_unverified_from` is optional (default `false`). Set it to `true` to let the rule's `from` conditions count even for unverified senders (see Sender verification).
- `mark_as_read` is optional (default `false`). Set it to `true` to mark mail moved by this rule as read. It only applies when the rule reaches `threshold`; mail sent to `UNSURE_FOLDER` stays unread. If the move fails, the mail is marked unread again so that it is retried.
- `mark_as_spam` is optional (default `false`). Set it to `true` to mark mail moved by this rule as spam: it gets the keywords `$Junk` (RFC 5788) and `Junk` (Thunderbird), and `$NotJunk` and `NonJunk` are removed. Like `mark_as_read`, it only applies when the rule reaches `threshold`. If the keywords cannot be set, the mail stays in place; if the move fails, `$Junk` and `Junk` are removed again so that it is retried. The server must allow custom keywords (`\*` in `PERMANENTFLAGS`), otherwise it may drop them silently.

Every rule needs a `folder` and at least one condition, and every condition needs a `field` and a `pattern`. Rule folders must already exist and must not be `SOURCE_FOLDER`. Unknown keys (such as typos) are rejected, and so is the old `{"address": "folder"}` format.

### Configuration files

`config/` contains `mapping.example.json`, `whitelist.example.json` and `blacklist.example.json` as templates. The program only reads `mapping.json`, `whitelist.json` and `blacklist.json`, so copy an example over the matching file and edit it. JSON has no comments, so keep notes elsewhere.

`mapping.json`, `whitelist.json` and `blacklist.json` are re-read on every run, so edits take effect at the next poll without a restart. A missing file counts as empty; invalid JSON or an invalid rule aborts the run. `compose.yaml` mounts the config directory (`CONFIG_DIR`, default `./config`) read-only at `/config`.

### Sender verification

`From` can be forged, so the whitelist and `from` rule conditions only apply to verified senders. A sender counts as verified when the `Authentication-Results` header confirms its domain:

- If the header contains a DMARC result, it decides: `dmarc=pass` is verified, anything else is not. DMARC pass means SPF or DKIM succeeded for the `From` domain.
- If the header contains no DMARC result (servers that only run DKIM checks, e.g. OpenDKIM without OpenDMARC), a `dkim=pass` whose signing domain `header.d` equals the `From` domain counts. Example: `dkim=pass … header.d=shopware.com` verifies `no-reply@shopware.com`. A signature for a subdomain (`header.d=mail.shopware.com`) or for another domain does not count, and senders confirmed only by SPF are not verified.

A whitelisted sender that is not verified is logged with `WHITELIST_UNVERIFIED` and goes through the blacklist and score checks. For unverified senders, `from` conditions add no score, and a matching `from` condition is logged as `RULE_SENDER_UNVERIFIED`. All other conditions still count. The blacklist needs no verification.

A rule with `"trust_unverified_from": true` counts its `from` conditions without verification. Use it only for rules whose folder is not more trusted than the inbox, such as `Trash` or `Newsletter`. There, a forged sender can at most hide its own mail. Leave it off for folders like invoices, banking or customers, where a forged `From` would make a phishing mail look legitimate.

A sender can add its own `Authentication-Results` header claiming `dmarc=pass`. Set `AUTHSERV_ID` to the name your mail server writes at the start of its header (e.g. `Authentication-Results: mail.example.org; dkim=pass …` → `AUTHSERV_ID=mail.example.org`). Only the topmost header with that name is then used, and all others are ignored, both for this check and for the spam score. This only protects you if your server adds its own header to every incoming mail; otherwise a forged header further down that uses your server's name could be taken as genuine. Without `AUTHSERV_ID`, all headers count and a warning is logged at startup.

Limits:
- Senders whose domain has neither DMARC nor a DKIM signature for the `From` domain never qualify.
- Verification confirms the domain, not the individual mailbox.
- The DKIM fallback only affects verification, not the spam score: without DMARC checking on your server, `DMARC_FAIL` and the DMARC pass bonus never occur. `SPF_FAIL` only occurs if your server writes SPF results (`spf=`) into the header.

Set `REQUIRE_DMARC_FOR_LISTS=false` to trust `From` as-is.

### Folders

`CLEAN_FOLDER` is required, and so is `UNSURE_FOLDER` as soon as an unsure zone is used. Source, target, clean and unsure folder must all differ, and no rule may point to the source folder. The names are compared the way the server sees them, so two spellings of the same folder count as equal: `INBOX` and `inbox` (INBOX is case-insensitive, also as the first level such as `inbox/Archive`), or `geschäftlich` and `gesch&AOQ-ftlich`.

Folder names can be written normally, including umlauts (e.g. `geschäftlich`); the program encodes them for the server (IMAP modified UTF-7). Names already in encoded form (e.g. `gesch&AOQ-ftlich`) are accepted unchanged. For this reason, a name containing `&` followed by letters, digits, `+` or `,` and a `-` is treated as already encoded; write a literal `&` as `&-` in that case.

#### Subfolders and the hierarchy separator

Folder names are sent to the server exactly as written; the program does not translate `/` into anything else. Subfolders must therefore be written with the separator your server uses between levels. Many servers use `/`, but others, e.g. Dovecot in many setups, use `.`:

| Folder as shown in Thunderbird | Server with `/` | Server with `.` |
|---|---|---|
| Inbox › Test | `INBOX/Test` | `INBOX.Test` |
| Finance › Invoices | `Finance/Invoices` | `Finance.Invoices` |
| Inbox › Server › backup01 | `INBOX/Server/backup01` | `INBOX.Server.backup01` |

Thunderbird always shows paths with `/`, so its folder names cannot be copied as they are. This applies to all folder settings (`SOURCE_FOLDER`, `TARGET_FOLDER`, `CLEAN_FOLDER`, `UNSURE_FOLDER`) and to the folders in `mapping.json`.

To find your server's separator, look at the `folder check:` lines at the start of a run: every missing folder is reported with `server hierarchy separator is "."` (or `"/"`). A wrong separator shows up like this:

- **`SOURCE_FOLDER`:** the server rejects the folder and every run fails, e.g. `run failed: IMAP command failed: … NO [CANNOT] Invalid mailbox name: Name must not have '/' characters`.
- **Destination folders** (`TARGET_FOLDER`, `CLEAN_FOLDER`, `UNSURE_FOLDER`, rule folders): the folder check reports them as missing, and moving a message there fails with `move to "…" failed, mail stays in place` (or `copy to …` on servers without MOVE). The message stays in the source folder and is retried an hour later, so this is easy to miss.

The case-insensitive `INBOX` comparison described above only covers `/` as separator (`inbox/Archive`); with `.`, write `INBOX` in upper case (`INBOX.Archive`).

## Moving

All destination folders (`TARGET_FOLDER`, `CLEAN_FOLDER`, `UNSURE_FOLDER` and rule folders) must already exist. At the start of every run, each missing folder is logged as `folder check: "<name>" does not exist on the server`, together with the settings or rules that use it and the server's hierarchy separator. Mail for a missing folder stays in `SOURCE_FOLDER` and is moved once the folder exists.

How mail is moved depends on what the server supports:

- **MOVE** (RFC 6851): the message is moved in one atomic step, so an interruption cannot leave a duplicate.
- **UIDPLUS without MOVE:** the message is copied, the original flagged deleted, and only that message expunged with `UID EXPUNGE`. Other deleted mail in the folder is not touched. If a step after the copy fails, the run stops with an error:
  - If the expunge fails, the original stays in `SOURCE_FOLDER` flagged deleted. Messages flagged deleted are no longer picked up, so this causes one duplicate, and the flagged original remains visible for cleanup.
  - If flagging the original fails, it is tried once more. If it still fails, the original stays unflagged next to its copy. The sorter logs `uid=… was copied to "…" but the original could not be flagged` and does not process that message again until the program restarts or the mailbox is recreated, so no further copies are made. Remove one of the two copies by hand.
- **Neither:** nothing is moved. The sorter logs `server supports neither MOVE nor UIDPLUS` once and behaves like dry run.

Every server command has a 30-second timeout, so a long run on a slow server is not cut off. Messages not marked `UNSEEN` are skipped. This version supports TLS IMAP with SASL PLAIN authentication; it does not support OAuth2.

## Log

At startup, one `settings: …` line shows the effective configuration. Every evaluated message then produces one line:

```
uid=<uid> sender=<address> verdict=<spam|unsure|mapped|clean> spam_score=<n> rule=<"name"> rule_score=<n> reasons=<list> folder="<folder>" mark_read=<true|false> mark_spam=<true|false> dry_run=<true|false>
```

`spam_score` is `-` for whitelisted and blacklisted mail, because the spam check is skipped for those. `rule` and `rule_score` show the best-scoring rule even if it stayed below the thresholds, which helps with tuning. Only rules with at least one matching condition count; a `from` condition ignored for an unverified sender does not. They are `-` when no rule matched or the rules were not consulted (spam, unsure spam score, or no rules). Empty values are written as `-`.

| Reason | Meaning |
|---|---|
| `DMARC_FAIL`, `SPF_FAIL`, `DKIM_FAIL` | Failed authentication result (spam score). |
| `REPLY_TO_DIFFERS` | Reply-To domain differs from the From domain (spam score). |
| `UPSTREAM_SPAM_FLAG`, `UPSTREAM_SPAM_STATUS` | Upstream spam filter says "yes" (spam score). |
| `SPAM_UNSURE` | Spam score in the `SPAM_UNSURE_SCORE` zone. |
| `WHITELISTED`, `BLACKLISTED` | Sender on the whitelist or blacklist. |
| `WHITELIST_UNVERIFIED` | Sender on the whitelist, but not verified. |
| `RULE:"name"=n` | Rule reached `threshold` with score n. |
| `RULE_UNSURE:"name"=n` | Best rule only reached `unsure_threshold`. |
| `RULE_SENDER_UNVERIFIED` | A `from` condition matched but did not count (sender not verified). |

Other log lines:
- `warning: AUTHSERV_ID is not set …`
- `folder check: …`: missing folder
- `folder check failed: …`: the folder list could not be read; the run continues without the check
- `uid=… was copied to "…" but the original could not be flagged …`: see Moving
- `warning: POLL_SECONDS …; using 300`: invalid `POLL_SECONDS` at startup
- `uid=… move to "…" failed …` / `uid=… copy to "…" failed …`: mail stays in place
- `uid=… fetch error: …`
- `server supports neither MOVE nor UIDPLUS …`
- `configuration error: …` (at startup) and `run failed: …`: the run was aborted, e.g. because of an invalid setting

## License

Mozilla Public License 2.0 (MPL-2.0), see [LICENSE](../../LICENSE).
