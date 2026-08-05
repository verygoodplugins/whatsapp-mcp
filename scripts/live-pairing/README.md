# Live pairing page (Windows)

WhatsApp pairing QR codes rotate every 20–60 seconds. If the terminal QR
renders badly (a common problem on Windows consoles with the default
codepage/raster fonts) or you take a screenshot of it, the code is usually
expired by the time you scan it, and the phone shows "Can't link device".

These scripts fix that by showing the QR in a browser page that refreshes
itself with the current code every 2 seconds — whenever you scan, the code
is valid.

## How it works

`pair-live.ps1` starts `whatsapp-bridge.exe` hidden with its output
redirected to a log file. whatsmeow's QRChannel debug logger writes each
rotating pairing payload as an `Emitting QR code <payload>` line; the
watcher picks up every new payload, renders it to SVG with
`payload2svg.py`, and rewrites `out/pair.html` (which carries a
`<meta http-equiv="refresh">`). When the bridge prints
`Successfully connected and authenticated!` the page flips to a success
state and the watcher exits, leaving the bridge running.

## Requirements

- A built bridge: `cd whatsapp-bridge && go build -o whatsapp-bridge.exe .`
- Python on `PATH` with the QR library: `pip install qrcode`

## Usage

Double-click `pair-whatsapp.cmd` (or run `pair-live.ps1` and open the
`out/pair.html` path it prints). Then on your phone: WhatsApp → Settings →
Linked Devices → Link a Device, and scan the code in the browser.

The watcher restarts the bridge automatically if a pairing window
expires, and gives up after 30 minutes without a scan. Generated
artifacts (log, SVG, HTML) live under `out/`, which is gitignored.
