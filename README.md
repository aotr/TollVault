# 🏛️ TollVault

A single-binary toll collection analytics platform with a web dashboard and Telegram bot integration.

---

## Features

- **📊 Dashboard** — Revenue, GST, and transaction slab breakdown at a glance
- **📂 CSV Upload** — Upload toll CSV files via web or Telegram bot
- **📅 History** — Filter by day/week/month/year with date-range pickers
- **🤖 Telegram Bot** — Upload CSVs, get instant reports via `/status`, `/today`, `/backup`
- **🗑️ Clear Last Upload** — Undo the most recent upload without losing history
- **🌗 Theme Toggle** — Light, Dark, and System modes
- **💾 Auto-Setup** — `config.json` and `data.db` auto-created on first run

## Quick Start

```bash
# Build
go build -o tollvault main.go

# Run (opens browser automatically)
./tollvault

# Run headless (server only, no browser)
./tollvault --headless
```

The app will:
1. Auto-create `config.json` with defaults
2. Auto-create `data.db` (SQLite)
3. Start the web server on `http://localhost:8080`

## Distribution

Only **2 files** needed to distribute:
| File | Purpose |
|------|---------|
| `tollvault` (or `.exe`) | The executable |
| `data.db` | SQLite database (auto-created) |

`config.json` is auto-generated on first run.

## Configuration

Edit `config.json` or use the **Settings** page (`/settings`):

```json
{
  "telegram_token": "YOUR_BOT_TOKEN",
  "admin_chat_id": -4021888055,
  "port": "8080",
  "db_path": "data.db"
}
```

## Telegram Bot Commands

| Command | Description |
|---------|-------------|
| `/start` | Welcome message |
| `/status` | All-time revenue report |
| `/today` | Today's transactions summary |
| `/backup` | Download the database file |
| *Send CSV* | Auto-processes and sends report |

## CSV Format

Your CSV must include these columns:

```
ENROLMENT_NO_DATE,TOTAL_AMOUNT_CHARGED,GST_AMOUNT,OPERATOR_ID,RESIDENT_NAME
```

## Cross-Platform Builds

```bash
# macOS
go build -o tollvault_mac main.go

# Windows
GOOS=windows GOARCH=amd64 go build -o tollvault.exe main.go
```

## Tech Stack

- **Go** — Single binary, no runtime dependencies
- **SQLite** (via `modernc.org/sqlite`) — Pure Go, no CGO
- **Embedded HTML/CSS** — All assets compiled into the binary
- **Telegram Bot API** — Real-time CSV upload and reporting

---

Built with ❤️ for toll collection operators.
