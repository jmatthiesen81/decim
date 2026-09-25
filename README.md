# Decim - IMAP Header Sorter

Sorts incoming mail on an IMAP server using only the message headers. It runs as a small Go service, one Docker container per mailbox, and moves each unread message from the source folder to a spam, unsure, rule-specific or clean folder.

- **Headers only:** fetches `BODY.PEEK[HEADER]`, so message bodies are never downloaded and mail stays unread (unless a rule sets `mark_as_read`).
- **Spam check:** a conservative score from SPF, DKIM and DMARC results and upstream spam flags, plus a whitelist and a blacklist.
- **Mapping rules:** scored header conditions in `config/mapping.json` route clean mail into folders, with an optional "unsure" zone.
- **Forgery-aware:** the whitelist and `from` rule conditions only apply to senders verified by DMARC or DKIM.
- **Private:** no mail data leaves your server; no external AI service is involved.

## Quick start

```sh
cp .env.example .env
# Set IMAP_DSN to your mailbox credentials, set CLEAN_FOLDER and check the folder names.
docker compose up -d --build
docker compose logs -f
```

The service starts in dry run (`DRY_RUN=true`) and only logs its decisions. Set `DRY_RUN=false` once they look right.

## Documentation

- [English](docs/en/README.md)
- [Deutsch](docs/de/README.md)

## License

[Mozilla Public License 2.0](LICENSE)
