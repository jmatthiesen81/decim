# IMAP Header Sorter

[English](../en/README.md) | Deutsch

Eigenständiger Go-Dienst: ein Container pro Postfach. Er ruft von ungelesenen Nachrichten nur `BODY.PEEK[HEADER]` ab, lädt also weder den Nachrichtentext herunter noch markiert er E-Mails als gelesen (außer eine Regel setzt `mark_as_read`). Die Prüfung des TLS-Zertifikats ist aktiviert. Es werden keine E-Mail-Daten an einen externen KI-Dienst gesendet.

## Start

Im Projektverzeichnis ausführen:

```sh
cp .env.example .env
# IMAP_DSN mit den Zugangsdaten des Postfachs füllen, CLEAN_FOLDER setzen und die Ordnernamen prüfen.
docker compose up -d --build
```

Lass `DRY_RUN=true` zunächst stehen und prüfe die Ausgabe von `docker compose logs -f`. Setze `DRY_RUN=false` erst, wenn die Entscheidungen stimmen. Halte die `.env` privat, sie enthält ein Passwort.

Im Testlauf wird nichts verschoben, alle Nachrichten bleiben ungelesen. Jede Nachricht wird einmal protokolliert und in den folgenden Durchläufen ausgelassen, damit neuere Nachrichten an die Reihe kommen. Nach einer Stunde, bei einer Änderung an `mapping.json`, `whitelist.json` oder `blacklist.json`, wenn ein Zielordner angelegt oder entfernt wird oder wenn der Container neu startet, werden alle Nachrichten erneut bewertet und protokolliert. Änderungen an den Regeln erscheinen also beim nächsten Durchlauf im Log.

## Docker

Der Dienst wird vollständig mit Docker gebaut und betrieben; Go muss auf dem Host nicht installiert sein.

### Wie das Image gebaut wird

Das `Dockerfile` hat zwei Stufen:

1. **Build-Stufe** (`golang:1.23-alpine`): kopiert `go.mod`, die `*.go`-Dateien und die Beispiel-Konfigurationsdateien, führt `go vet` und alle Tests aus und kompiliert anschließend eine einzelne statische Binärdatei (`CGO_ENABLED=0`, `-trimpath`, verkleinert mit `-ldflags='-s -w'`). Ein fehlschlagender Test bricht den Build ab; eine fehlerhafte Version wird also nie zum Image.
2. **Laufzeit-Stufe** (`scratch`, ein leeres Image): enthält nur die kompilierte Binärdatei `/sorter` und die CA-Zertifikate, die zur Prüfung des TLS-Zertifikats des Servers nötig sind. Im fertigen Image gibt es keine Shell, keinen Paketmanager und keine Go-Werkzeuge.

Der Container läuft als unprivilegierter Benutzer `65532:65532`. Er öffnet keine Ports; er braucht nur eine ausgehende Verbindung zum IMAP-Server (Port 993). Sein einziger Zustand liegt im Arbeitsspeicher, außer dem Konfigurationsverzeichnis ist also kein Volume nötig.

### Was Compose macht

`compose.yaml` definiert einen Dienst, `sorter`:

- `build: .`: baut das Image aus dem `Dockerfile` im Projektverzeichnis.
- `env_file`: übergibt die Einstellungen aus der `.env` (oder `ENV_FILE`) als Umgebungsvariablen an den Container.
- `volumes`: bindet das Konfigurationsverzeichnis (`./config` oder `CONFIG_DIR`) schreibgeschützt unter `/config` ein, wo das Programm `mapping.json`, `whitelist.json` und `blacklist.json` liest.
- `restart: unless-stopped`: startet den Container nach einem Absturz oder einem Neustart des Docker-Hosts neu, außer du hast ihn selbst gestoppt.

Das Programm läuft in einer Endlosschleife: ein Durchlauf alle `POLL_SECONDS`, bis der Container gestoppt wird. Alle Ausgaben gehen nach stdout, `docker compose logs` zeigt sie also an. Das Image enthält keine Zeitzonendaten, die Zeiten im Log sind daher in UTC.

### Häufige Befehle

Alle Befehle im Projektverzeichnis ausführen.

| Aufgabe | Befehl |
|---|---|
| Bauen und starten (erstmals, nach einer Codeänderung oder `git pull`) | `docker compose up -d --build` |
| Log verfolgen | `docker compose logs -f` |
| Geänderte `.env` übernehmen | `docker compose up -d` (erstellt den Container neu, ohne neu zu bauen) |
| Geänderte Dateien in `config/` übernehmen | nichts, sie werden beim nächsten Durchlauf neu eingelesen |
| Status anzeigen | `docker compose ps` |
| Neu starten | `docker compose restart` |
| Stoppen und Container entfernen | `docker compose down` |
| Nur bauen und Tests ausführen | `docker compose build` |

Die Dateien im Konfigurationsverzeichnis müssen für den Container-Benutzer (UID 65532) lesbar sein. Die Standardrechte neuer Dateien (`644`) passen; eine nur für den Eigentümer lesbare Datei (`600`) kann nicht gelesen werden und bricht den Durchlauf ab.

### Mehrere Postfächer

Jedes Postfach braucht einen eigenen Container mit eigenen Einstellungen und eigenem Konfigurationsverzeichnis. Am einfachsten ist eine eigene Kopie des Projektverzeichnisses pro Postfach. Um mehrere Postfächer aus einem Verzeichnis zu betreiben, bekommt stattdessen jedes einen Compose-Projektnamen und eine eigene Env-Datei:

```sh
# postfach2.env enthält alle Einstellungen für das zweite Postfach, außerdem:
#   ENV_FILE=postfach2.env
#   CONFIG_DIR=./config-postfach2
docker compose -p postfach2 --env-file postfach2.env up -d --build
docker compose -p postfach2 --env-file postfach2.env logs -f
```

Mit `--env-file` liest Compose `ENV_FILE` und `CONFIG_DIR` aus dieser Datei; der Container bekommt also die Einstellungen und das Konfigurationsverzeichnis des zweiten Postfachs. `-p` und `--env-file` bei jedem Befehl für dieses Postfach angeben.

### Ohne Docker ausführen (Entwicklung)

Mit installiertem Go ab Version 1.23 laufen Tests und Programm auch direkt:

```sh
go vet ./... && go test ./...

set -a; . ./.env; set +a
MAPPING_FILE=config/mapping.json WHITELIST_FILE=config/whitelist.json BLACKLIST_FILE=config/blacklist.json go run .
```

Die Dateipfade sind nötig, weil die Standardwerte auf `/config` zeigen, das nur im Container existiert.

## Einstellungen

Alle Einstellungen sind Umgebungsvariablen aus der `.env`. Änderungen erfordern `docker compose up -d`.

| Variable | Standard | Bedeutung |
|---|---|---|
| `IMAP_DSN` | – (Pflicht) | `imaps://benutzer:passwort@host:993`. Sonderzeichen in Benutzer und Passwort URL-kodieren (`@` → `%40`, `:` → `%3A`, `/` → `%2F`). Standard-Port ist 993. |
| `DRY_RUN` | `true` | Entscheidungen nur protokollieren, nichts verschieben. |
| `SOURCE_FOLDER` | `INBOX` | Ordner, dessen ungelesene Nachrichten sortiert werden. |
| `TARGET_FOLDER` | `Junk` | Spam-Ordner. |
| `CLEAN_FOLDER` | – (Pflicht) | Ordner für saubere Nachrichten, die keine Regel übernimmt. |
| `UNSURE_FOLDER` | – | Ordner für Nachrichten, bei denen sich der Sortierer nicht sicher ist. Pflicht, wenn `unsure_threshold` oder `SPAM_UNSURE_SCORE` verwendet wird. |
| `SPAM_SCORE` | `5` | Spam-Bewertung, ab der eine Nachricht Spam ist. Muss mindestens 1 sein. |
| `SPAM_UNSURE_SCORE` | aus | Spam-Bewertungen ab diesem Wert bis unter `SPAM_SCORE` gehen nach `UNSURE_FOLDER`. Muss zwischen 1 und `SPAM_SCORE`-1 liegen. |
| `POLL_SECONDS` | `300` | Sekunden zwischen den Durchläufen, mindestens 30. Ein ungültiger oder kleinerer Wert wird durch 300 ersetzt, mit einer Warnung beim Start. |
| `MAX_PER_RUN` | `100` | Höchstzahl Nachrichten pro Durchlauf, älteste zuerst. Nachrichten, die in einem früheren Durchlauf im Quellordner geblieben sind, werden ausgelassen (siehe Start). `0` verarbeitet nichts. |
| `REQUIRE_DMARC_FOR_LISTS` | `true` | Whitelist und `from`-Bedingungen der Regeln nur bei bestätigten Absendern anwenden (`dmarc=pass` oder, wenn der Server kein DMARC-Ergebnis liefert, ein passendes `dkim=pass`; siehe Absenderprüfung). |
| `AUTHSERV_ID` | – | Name, den dein Mailserver in seinen `Authentication-Results`-Headern verwendet, z. B. `mail.example.org`. Empfohlen, siehe Absenderprüfung. |
| `MAPPING_FILE` | `/config/mapping.json` | Pfad der Regeldatei im Container. |
| `WHITELIST_FILE` | `/config/whitelist.json` | Pfad der Whitelist im Container. |
| `BLACKLIST_FILE` | `/config/blacklist.json` | Pfad der Blacklist im Container. |
| `CONFIG_DIR` | `./config` | Verzeichnis auf dem Host, das `compose.yaml` unter `/config` einbindet. Wird von Docker Compose gelesen, nicht vom Programm (siehe Mehrere Postfächer). |
| `ENV_FILE` | `.env` | Env-Datei, die `compose.yaml` an den Container übergibt. Wird von Docker Compose gelesen, nicht vom Programm; nur für mehrere Postfächer in einem Verzeichnis nötig. |

Schalter akzeptieren `true`/`false`, `t`/`f` und `1`/`0` in beliebiger Schreibweise (z. B. `TRUE` oder `False`). Jeder andere Wert, etwa ein Schalter auf `no` oder eine Zahl wie `5,5`, wird als Fehler gemeldet und bricht den Durchlauf ab, statt stillschweigend ersetzt zu werden. Einzige Ausnahme ist `POLL_SECONDS`, das auf 300 zurückfällt (siehe oben); ein Tippfehler kann den Container also nicht in eine Neustart-Schleife bringen.

## Klassifizierung

Die Spam-Bewertung ist bewusst zurückhaltend. DMARC fail ergibt 4 Punkte, SPF fail 2, DKIM fail 1, eine abweichende Reply-To-Domain 1 und ein vorgelagertes `X-Spam-Flag` oder `X-Spam-Status` mit „yes“ jeweils 5. DMARC pass zieht 2 Punkte ab. Beim Standard-Schwellenwert von 5 braucht eine Nachricht mehrere Signale, um als Spam zu gelten, z. B. DMARC fail plus DKIM fail oder eine vorgelagerte Spam-Markierung ohne DMARC pass. Mit DMARC pass erreicht eine vorgelagerte Spam-Markierung allein nur 3 Punkte und ist kein Spam.

Die Spam-Bewertung ignoriert den Betreff (nur die Mapping-Regeln nutzen ihn). SPF-, DKIM- und DMARC-Ergebnisse stammen aus den `Authentication-Results`-Headern, die bereits in der Nachricht stehen; setze `AUTHSERV_ID`, damit nur die Ergebnisse deines eigenen Servers zählen (siehe Absenderprüfung). Modelltraining oder ein LLM sind noch nicht enthalten.

## Sortierung

Jede ungelesene Nachricht in `SOURCE_FOLDER` wird zuerst eingestuft, nach der ersten zutreffenden Prüfung:

1. Absender in `config/whitelist.json`: sauber, ohne Spam-Prüfung.
2. Absender in `config/blacklist.json`: Spam.
3. Sonst entscheidet die Header-Bewertung: ab `SPAM_SCORE` Spam; ab dem optionalen `SPAM_UNSURE_SCORE` unsicher; darunter sauber.

Spam wird mit denselben Schlüsselwörtern wie bei `mark_as_spam` markiert (siehe Mapping-Regeln) und nach `TARGET_FOLDER` verschoben, unsichere Nachrichten nach `UNSURE_FOLDER`. Saubere Nachrichten werden über die Regeln in `config/mapping.json` einsortiert (siehe Mapping-Regeln), sonst landen sie in `CLEAN_FOLDER`. Die Regeln retten also nie Spam.

Beide Listen sind JSON-Arrays mit E-Mail-Adressen und werden ohne Beachtung der Groß-/Kleinschreibung mit der Adresse im `From`-Header verglichen. Einträge dürfen wie Regelmuster die Platzhalter `*` und `%` enthalten, die beliebige Zeichen abdecken: `%@spammer.example` erfasst eine ganze Domain (aber keine Subdomains), `offers-*@shop.example` alle Adressen, die mit `offers-` beginnen:

```json
["alice@example.com", "newsletter@example.org", "%@spammer.example"]
```

### Mapping-Regeln

`config/mapping.json` enthält Regeln. Jede Regel hat einen Zielordner und eine Liste von Bedingungen. Eine Bedingung vergleicht ein Feld mit einem Muster und addiert bei einem Treffer ihre Punkte (auch negative). Die Regel mit der höchsten Punktzahl entscheidet:

- Punkte ab `threshold` (Standard 5): Verschiebung in den Ordner der Regel
- Punkte ab `unsure_threshold` (optional, zwischen 1 und `threshold`-1): Verschiebung nach `UNSURE_FOLDER`
- sonst: Verschiebung nach `CLEAN_FOLDER`

Bei Gleichstand gewinnt die zuerst aufgeführte Regel.

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
        { "field": "subject", "pattern": "*rechnung*", "score": 3 },
        { "field": "subject", "pattern": "*mahnung*", "score": -2 }
      ]
    }
  ]
}
```

Bei bestätigtem Absender erreicht eine Rechnung von `shop.example` 6 Punkte und landet in `Finance/Invoices`. Jede andere Nachricht von `shop.example` oder eine Rechnung eines anderen Absenders erreicht 3 Punkte und landet in `UNSURE_FOLDER`. Eine Rechnung mit „Mahnung“ im Betreff von `shop.example` erreicht 4 Punkte und landet ebenfalls in `UNSURE_FOLDER`. Ohne Bestätigung bringt die `from`-Bedingung 0 Punkte (siehe Absenderprüfung); die Rechnung von `shop.example` erreicht dann nur 3 Punkte und landet in `UNSURE_FOLDER`.

Die Ordnernamen in dieser README und in `mapping.example.json` trennen Ebenen mit `/`. Auf einem Server, der stattdessen `.` verwendet, schreibt man `Finance.Invoices` (siehe Ordner).

- `field`: `from` ist die Absenderadresse; `subject` oder jeder andere Header-Name (z. B. `to`, `list-unsubscribe`) wird mit dem vollständigen Header-Wert verglichen. Kodierte Header wie UTF-8-Betreffzeilen werden vorher dekodiert. Kommt ein Header mehrfach vor, werden die Werte mit Leerzeichen zu einem Wert verbunden. Eine Bedingung auf einen fehlenden Header trifft nie zu.
- `pattern`: `*` und `%` stehen für beliebige Zeichen, Groß-/Kleinschreibung wird ignoriert, und das Muster muss den ganzen Wert abdecken. `%@example.com` erfasst eine ganze Domain (aber keine Subdomains), `*rechnung*` trifft „Rechnung“ an beliebiger Stelle im Betreff, und ein Muster ohne Platzhalter muss exakt passen.
- `name` ist optional und erscheint im Log, z. B. `RULE:"Invoices"=6` oder `RULE_UNSURE:"Invoices"=3`. Regeln ohne Namen erscheinen nach ihrer Position als `rule 1`, `rule 2` usw.
- `trust_unverified_from` ist optional (Standard `false`). Mit `true` zählen die `from`-Bedingungen der Regel auch bei nicht bestätigten Absendern (siehe Absenderprüfung).
- `mark_as_read` ist optional (Standard `false`). Mit `true` werden Nachrichten, die diese Regel verschiebt, als gelesen markiert. Das gilt nur, wenn die Regel `threshold` erreicht; Nachrichten für `UNSURE_FOLDER` bleiben ungelesen. Schlägt das Verschieben fehl, wird die Nachricht wieder als ungelesen markiert, damit sie erneut versucht wird.
- `mark_as_spam` ist optional (Standard `false`). Mit `true` werden Nachrichten, die diese Regel verschiebt, als Spam markiert: Sie erhalten die Schlüsselwörter `$Junk` (RFC 5788) und `Junk` (Thunderbird), `$NotJunk` und `NonJunk` werden entfernt. Wie `mark_as_read` gilt das nur, wenn die Regel `threshold` erreicht. Lassen sich die Schlüsselwörter nicht setzen, bleibt die Nachricht liegen; schlägt das Verschieben fehl, werden `$Junk` und `Junk` wieder entfernt, damit sie erneut versucht wird. Der Server muss eigene Schlüsselwörter erlauben (`\*` in `PERMANENTFLAGS`), sonst verwirft er sie womöglich stillschweigend. Manche Server entfernen Schlüsselwörter außerdem beim Verschieben in den Spam-Ordner (z. B. durch einen Spam-Trainings-Hook), deshalb werden sie am Ende jedes Durchlaufs auf den verschobenen Kopien erneut gesetzt, sofern der Server deren neue UID meldet (`COPYUID`, UIDPLUS). Als Spam eingestufte Nachrichten (Blacklist oder Header-Bewertung) erhalten diese Schlüsselwörter immer, auch ohne Regel.

Jede Regel braucht einen `folder` und mindestens eine Bedingung, jede Bedingung ein `field` und ein `pattern`. Die Ordner der Regeln müssen bereits existieren und dürfen nicht `SOURCE_FOLDER` sein. Unbekannte Schlüssel (etwa Tippfehler) werden abgelehnt, ebenso das alte Format `{"adresse": "ordner"}`.

### Konfigurationsdateien

`config/` enthält `mapping.example.json`, `whitelist.example.json` und `blacklist.example.json` als Vorlagen. Das Programm liest nur `mapping.json`, `whitelist.json` und `blacklist.json`; kopiere also eine Vorlage über die passende Datei und passe sie an. JSON kennt keine Kommentare, Notizen gehören daher woandershin.

`mapping.json`, `whitelist.json` und `blacklist.json` werden bei jedem Durchlauf neu eingelesen, Änderungen gelten also ab dem nächsten Abruf ohne Neustart. Eine fehlende Datei gilt als leer; ungültiges JSON oder eine ungültige Regel bricht den Durchlauf ab. `compose.yaml` bindet das Konfigurationsverzeichnis (`CONFIG_DIR`, Standard `./config`) schreibgeschützt unter `/config` ein.

### Absenderprüfung

`From` kann gefälscht werden. Deshalb greifen die Whitelist und `from`-Bedingungen der Regeln nur bei bestätigten Absendern. Ein Absender gilt als bestätigt, wenn der `Authentication-Results`-Header seine Domain bestätigt:

- Enthält der Header ein DMARC-Ergebnis, entscheidet dieses: `dmarc=pass` ist bestätigt, alles andere nicht. DMARC pass bedeutet, dass SPF oder DKIM für die `From`-Domain erfolgreich war.
- Enthält der Header kein DMARC-Ergebnis (Server, die nur DKIM prüfen, z. B. OpenDKIM ohne OpenDMARC), zählt ein `dkim=pass`, dessen signierende Domain `header.d` gleich der `From`-Domain ist. Beispiel: `dkim=pass … header.d=shopware.com` bestätigt `no-reply@shopware.com`. Eine Signatur für eine Subdomain (`header.d=mail.shopware.com`) oder eine andere Domain zählt nicht, und nur per SPF bestätigte Absender gelten nicht als bestätigt.

Ein nicht bestätigter Whitelist-Absender wird mit `WHITELIST_UNVERIFIED` protokolliert und durchläuft Blacklist und Bewertung. Bei nicht bestätigten Absendern bringen `from`-Bedingungen keine Punkte, und eine zutreffende `from`-Bedingung wird als `RULE_SENDER_UNVERIFIED` protokolliert. Alle anderen Bedingungen zählen weiterhin. Solche Nachrichten landen in `UNSURE_FOLDER` (falls gesetzt) statt in `CLEAN_FOLDER`, denn sie sind entweder gefälscht oder stammen vom echten Absender mit fehlerhafter Signatur; erreichen die übrigen Bedingungen trotzdem den `threshold` einer Regel, greift die Regel wie gewohnt. Die Blacklist benötigt keine Prüfung.

Eine Regel mit `"trust_unverified_from": true` wertet ihre `from`-Bedingungen ohne Prüfung. Setze das nur bei Regeln, deren Ordner nicht vertrauenswürdiger ist als der Posteingang, etwa `Trash` oder `Newsletter`. Dort kann ein gefälschter Absender höchstens seine eigene Nachricht verstecken. Bei Ordnern wie Rechnungen, Bank oder Kunden bleibt es aus, weil ein gefälschtes `From` dort eine Phishing-Nachricht glaubwürdig erscheinen ließe.

Ein Absender kann selbst einen `Authentication-Results`-Header mit `dmarc=pass` einfügen. Setze `AUTHSERV_ID` auf den Namen, den dein Mailserver an den Anfang seines Headers schreibt (z. B. `Authentication-Results: mail.example.org; dkim=pass …` → `AUTHSERV_ID=mail.example.org`). Dann zählt nur der oberste Header mit diesem Namen, alle anderen werden ignoriert, sowohl für diese Prüfung als auch für die Spam-Bewertung. Das schützt nur, wenn dein Server jeder eingehenden Nachricht einen eigenen Header hinzufügt; sonst könnte ein weiter unten stehender, gefälschter Header mit dem Namen deines Servers als echt gelten. Ohne `AUTHSERV_ID` zählen alle Header, und beim Start wird eine Warnung protokolliert.

Einschränkungen:
- Absender ohne DMARC und ohne DKIM-Signatur für die `From`-Domain qualifizieren sich nie.
- Die Prüfung bestätigt die Domain, nicht das einzelne Postfach.
- Die DKIM-Ersatzprüfung wirkt nur auf die Absenderprüfung, nicht auf die Spam-Bewertung: Ohne DMARC-Prüfung auf deinem Server kommen `DMARC_FAIL` und der Abzug für DMARC pass nie vor. `SPF_FAIL` gibt es nur, wenn dein Server SPF-Ergebnisse (`spf=`) in den Header schreibt.

Mit `REQUIRE_DMARC_FOR_LISTS=false` wird `From` ungeprüft vertraut.

### Ordner

`CLEAN_FOLDER` ist Pflicht, `UNSURE_FOLDER` ebenfalls, sobald eine Unsicher-Zone verwendet wird. Quell-, Ziel-, Clean- und Unsure-Ordner müssen sich unterscheiden, und keine Regel darf auf den Quellordner zeigen. Die Namen werden so verglichen, wie der Server sie sieht; zwei Schreibweisen desselben Ordners gelten also als gleich: `INBOX` und `inbox` (INBOX ist unabhängig von Groß-/Kleinschreibung, auch als erste Ebene wie `inbox/Archiv`) oder `geschäftlich` und `gesch&AOQ-ftlich`.

Ordnernamen können normal geschrieben werden, auch mit Umlauten (z. B. `geschäftlich`); das Programm kodiert sie für den Server (IMAP modified UTF-7). Bereits kodierte Namen (z. B. `gesch&AOQ-ftlich`) werden unverändert übernommen. Deshalb gilt ein Name, der `&` gefolgt von Buchstaben, Ziffern, `+` oder `,` und einem `-` enthält, als bereits kodiert; ein echtes `&` schreibt man in diesem Fall als `&-`.

#### Unterordner und das Trennzeichen

Ordnernamen werden genau so an den Server geschickt, wie sie geschrieben sind; das Programm übersetzt `/` nicht. Unterordner müssen daher mit dem Trennzeichen geschrieben werden, das dein Server zwischen den Ebenen verwendet. Viele Server nutzen `/`, andere, z. B. Dovecot in vielen Installationen, nutzen `.`:

| Ordner, wie Thunderbird ihn zeigt | Server mit `/` | Server mit `.` |
|---|---|---|
| Posteingang › Test | `INBOX/Test` | `INBOX.Test` |
| Finance › Invoices | `Finance/Invoices` | `Finance.Invoices` |
| Posteingang › Server › backup01 | `INBOX/Server/backup01` | `INBOX.Server.backup01` |

Thunderbird zeigt Pfade immer mit `/`; seine Ordnernamen lassen sich also nicht unverändert übernehmen. Das gilt für alle Ordner-Einstellungen (`SOURCE_FOLDER`, `TARGET_FOLDER`, `CLEAN_FOLDER`, `UNSURE_FOLDER`) und für die Ordner in `mapping.json`.

Das Trennzeichen deines Servers zeigen die `folder check:`-Zeilen zu Beginn eines Durchlaufs: Jeder fehlende Ordner wird mit `server hierarchy separator is "."` (oder `"/"`) gemeldet. Ein falsches Trennzeichen zeigt sich so:

- **`SOURCE_FOLDER`:** Der Server lehnt den Ordner ab, und jeder Durchlauf schlägt fehl, z. B. `run failed: IMAP command failed: … NO [CANNOT] Invalid mailbox name: Name must not have '/' characters`.
- **Zielordner** (`TARGET_FOLDER`, `CLEAN_FOLDER`, `UNSURE_FOLDER`, Ordner der Regeln): Die Ordnerprüfung meldet sie als fehlend, und das Verschieben schlägt mit `move to "…" failed, mail stays in place` fehl (bzw. `copy to …` auf Servern ohne MOVE). Die Nachricht bleibt im Quellordner und wird eine Stunde später erneut versucht; das fällt also leicht nicht auf.

Der oben beschriebene Vergleich von `INBOX` unabhängig von der Groß-/Kleinschreibung deckt nur `/` als Trennzeichen ab (`inbox/Archiv`); bei `.` schreibt man `INBOX` in Großbuchstaben (`INBOX.Archiv`).

## Verschieben

Alle Zielordner (`TARGET_FOLDER`, `CLEAN_FOLDER`, `UNSURE_FOLDER` und die Ordner der Regeln) müssen bereits existieren. Zu Beginn jedes Durchlaufs wird jeder fehlende Ordner als `folder check: "<name>" does not exist on the server` protokolliert, zusammen mit der Einstellung bzw. den Regeln, die ihn verwenden, und dem Trennzeichen des Servers. Nachrichten für einen fehlenden Ordner bleiben in `SOURCE_FOLDER` und werden verschoben, sobald der Ordner existiert.

Wie verschoben wird, hängt davon ab, was der Server unterstützt:

- **MOVE** (RFC 6851): Die Nachricht wird in einem atomaren Schritt verschoben; eine Unterbrechung kann kein Duplikat hinterlassen.
- **UIDPLUS ohne MOVE:** Die Nachricht wird kopiert, das Original als gelöscht markiert und nur diese Nachricht mit `UID EXPUNGE` entfernt. Andere gelöschte Nachrichten im Ordner bleiben unberührt. Schlägt ein Schritt nach dem Kopieren fehl, bricht der Durchlauf mit einem Fehler ab:
  - Schlägt das Expunge fehl, bleibt das Original als gelöscht markiert in `SOURCE_FOLDER`. Als gelöscht markierte Nachrichten werden nicht mehr aufgegriffen; es entsteht also ein Duplikat, und das markierte Original bleibt zum Aufräumen sichtbar.
  - Schlägt das Markieren fehl, wird es ein zweites Mal versucht. Klappt es wieder nicht, bleibt das Original unmarkiert neben seiner Kopie. Der Sortierer protokolliert `uid=… was copied to "…" but the original could not be flagged` und verarbeitet diese Nachricht erst nach einem Neustart des Programms oder einer Neuanlage des Postfachs wieder; es entstehen also keine weiteren Kopien. Entferne eine der beiden Kopien von Hand.
- **Keines von beiden:** Es wird nichts verschoben. Der Sortierer protokolliert einmal `server supports neither MOVE nor UIDPLUS` und arbeitet wie im Testlauf.

Jeder Server-Befehl hat ein Zeitlimit von 30 Sekunden; ein langer Durchlauf auf einem langsamen Server wird also nicht abgeschnitten. Nachrichten ohne `UNSEEN`-Markierung werden übersprungen. Diese Version unterstützt TLS-IMAP mit SASL-PLAIN-Authentifizierung, jedoch kein OAuth2.

## Log

Beim Start zeigt eine Zeile `settings: …` die wirksame Konfiguration. Danach erzeugt jede bewertete Nachricht eine Zeile:

```
uid=<uid> sender=<adresse> verdict=<spam|unsure|mapped|clean> spam_score=<n> rule=<"name"> rule_score=<n> reasons=<liste> folder="<ordner>" mark_read=<true|false> mark_spam=<true|false> dry_run=<true|false>
```

`spam_score` ist `-` bei Whitelist- und Blacklist-Absendern, weil die Spam-Prüfung für sie entfällt. `rule` und `rule_score` zeigen die Regel mit der höchsten Punktzahl, auch wenn sie unter den Schwellenwerten blieb; das hilft beim Feinabstimmen. Es zählen nur Regeln mit mindestens einer passenden Bedingung; eine `from`-Bedingung, die bei einem nicht verifizierten Absender ignoriert wird, zählt nicht. Sie sind `-`, wenn keine Regel passte oder die Regeln nicht befragt wurden (Spam, unsichere Spam-Bewertung oder keine Regeln). Leere Werte werden als `-` geschrieben.

| Grund | Bedeutung |
|---|---|
| `DMARC_FAIL`, `SPF_FAIL`, `DKIM_FAIL` | Fehlgeschlagene Authentifizierung (Spam-Bewertung). |
| `REPLY_TO_DIFFERS` | Reply-To-Domain weicht von der From-Domain ab (Spam-Bewertung). |
| `UPSTREAM_SPAM_FLAG`, `UPSTREAM_SPAM_STATUS` | Vorgelagerter Spamfilter meldet „yes“ (Spam-Bewertung). |
| `SPAM_UNSURE` | Spam-Bewertung in der `SPAM_UNSURE_SCORE`-Zone. |
| `WHITELISTED`, `BLACKLISTED` | Absender auf Whitelist bzw. Blacklist. |
| `WHITELIST_UNVERIFIED` | Absender auf der Whitelist, aber nicht bestätigt. |
| `RULE:"name"=n` | Regel hat `threshold` mit n Punkten erreicht. |
| `RULE_UNSURE:"name"=n` | Beste Regel hat nur `unsure_threshold` erreicht. |
| `RULE_SENDER_UNVERIFIED` | Eine `from`-Bedingung traf zu, zählte aber nicht (Absender nicht bestätigt). Nachrichten, die sonst nach `CLEAN_FOLDER` gingen, landen in `UNSURE_FOLDER`. |

Weitere Log-Zeilen:
- `warning: AUTHSERV_ID is not set …`
- `folder check: …`: fehlender Ordner
- `folder check failed: …`: Die Ordnerliste konnte nicht gelesen werden; der Durchlauf läuft ohne die Prüfung weiter
- `uid=… was copied to "…" but the original could not be flagged …`: siehe Verschieben
- `warning: POLL_SECONDS …; using 300`: ungültiges `POLL_SECONDS` beim Start
- `uid=… move to "…" failed …` / `uid=… copy to "…" failed …`: Nachricht bleibt, wo sie ist
- `uid=… fetch error: …`
- `server supports neither MOVE nor UIDPLUS …`
- `configuration error: …` (beim Start) und `run failed: …`: Durchlauf abgebrochen, z. B. wegen einer ungültigen Einstellung

## Lizenz

Mozilla Public License 2.0 (MPL-2.0), siehe [LICENSE](../../LICENSE).
